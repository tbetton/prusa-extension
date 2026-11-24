// Command filament-profile is a Prusa Connect module that writes PrusaSlicer
// 2.9 filament profiles for the spools loaded on a printer's tools. It reads
// the profiles Prusa Research and other sources publish, finds the nearest
// preset for each spool that fits the printer, and writes one Config Bundle
// (.ini) a person imports into PrusaSlicer.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

const (
	name    = "filament-profile"
	release = extension.Version("0.1.4")
)

// Settings is what an operator writes under this instance's config block. The
// defaults live in module.yaml.
type Settings struct {
	CatalogueURL string `json:"catalogue_url"`
	RefreshDays  int    `json:"refresh_days"`
	Sources      string `json:"sources"`
	PolymakerURL string `json:"polymaker_url"`
	CustomURLs   string `json:"custom_urls"`
}

func (s Settings) withDefaults() Settings {
	if strings.TrimSpace(s.CatalogueURL) == "" {
		s.CatalogueURL = defaultCatalogueURL
	}

	if strings.TrimSpace(s.PolymakerURL) == "" {
		s.PolymakerURL = defaultPolymakerURL
	}

	if s.RefreshDays <= 0 {
		s.RefreshDays = 7
	}

	return s
}

//go:embed openapi.json
var openAPI []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

// module holds the downloaded profiles and the settings.
type module struct {
	mu         sync.Mutex
	conn       *wirerpc.Conn
	ctx        context.Context
	settings   Settings
	dir        string
	configured bool
	catalogue  *Catalogue
	updated    time.Time
	refreshing bool
	lastError  string
	client     *http.Client
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, name+": "+format+"\n", args...)
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return fmt.Errorf("render the handshake: %w", err)
	}

	if _, err = os.Stdout.WriteString(line); err != nil {
		return fmt.Errorf("write the handshake: %w", err)
	}

	m := &module{ctx: ctx, client: &http.Client{Timeout: 3 * time.Minute}}

	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{
		Role:    wirerpc.RoleChild,
		Methods: m.methods(),
	})
	if err != nil {
		return fmt.Errorf("build the session: %w", err)
	}

	m.conn = conn

	logf("%s is serving on stdio as pid %d", release, os.Getpid())

	if err = conn.Serve(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

func (m *module) methods() wirerpc.Methods {
	return wirerpc.Methods{
		wirerpc.MethodInfo: func(context.Context, wirerpc.Call) (any, error) {
			return routesapi.Describe(
				wirerpc.Info{
					Name:     name,
					Version:  release,
					Contract: wirerpc.Contract,
					Serves:   []wirerpc.Method{wirerpc.MethodConfigure},
				}, openAPI)
		},
		wirerpc.MethodConfigure: wirerpc.ConfigureGranted(m.configure),
		wirerpc.MethodShutdown: func(context.Context, wirerpc.Call) (any, error) {
			logf("asked to stop")

			return nil, nil
		},
		routesapi.MethodServe: routesapi.Serve(m.routes()),
	}
}

// configure reads the sources kept in the data directory, and downloads them
// when one is missing or older than refresh_days.
func (m *module) configure(_ context.Context, sent Settings, grants wirerpc.Grants) error {
	settings := sent.withDefaults()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.configured {
		return errors.New("filament-profile: the settings were sent twice")
	}

	if grants.Data == "" {
		return errors.New("filament-profile: a persistent data directory is required")
	}

	m.settings, m.dir, m.configured = settings, grants.Data, true

	if c, err := assemble(grants.Data, settings); err == nil {
		m.catalogue = c

		if info, err := os.Stat(filepath.Join(grants.Data, bundleFile)); err == nil {
			m.updated = info.ModTime()
		}
	}

	if stale(grants.Data, settings) {
		m.refreshing = true

		go m.refresh()
	}

	return nil
}

// refresh downloads the sources and swaps the catalogue in. Callers set
// m.refreshing first, so two downloads never run together. A source that fails
// is reported and the others still load.
func (m *module) refresh() {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Minute)
	defer cancel()

	problems := fetchSources(ctx, m.client, m.dir, m.settings)
	c, err := assemble(m.dir, m.settings)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.refreshing = false

	if err != nil {
		problems = append(problems, err.Error())
	}

	m.lastError = strings.Join(problems, "; ")

	for _, problem := range problems {
		logf("catalogue refresh: %s", problem)
	}

	if err != nil {
		return
	}

	m.catalogue, m.updated = c, time.Now()
	logf("catalogue %s loaded: %d filament sections, %d printers", c.Version, c.Len(), len(c.printerOrder))
}

// status is the answer of GET /catalogue.
type status struct {
	Ready      bool           `json:"ready"`
	Refreshing bool           `json:"refreshing"`
	Version    string         `json:"version,omitempty"`
	Presets    int            `json:"presets"`
	Sources    map[string]int `json:"sources,omitempty"`
	Enabled    []string       `json:"enabled,omitempty"`
	UpdatedAt  *time.Time     `json:"updated_at,omitempty"`
	Error      string         `json:"error,omitempty"`
}

func (m *module) status() status {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := status{Refreshing: m.refreshing, Error: m.lastError}
	if m.catalogue != nil {
		s.Ready, s.Version, s.Presets = true, m.catalogue.Version, len(m.catalogue.Presets())
		s.Sources, s.Enabled = m.catalogue.Counts(), parseSources(m.settings.Sources)
		updated := m.updated
		s.UpdatedAt = &updated
	}

	return s
}

// ready answers the catalogue, or refuses the request while there is none.
func (m *module) ready(w http.ResponseWriter, r *http.Request) *Catalogue {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.configured {
		refuse(w, r, NotReady.New("the module has not been configured yet"))

		return nil
	}

	if m.catalogue == nil {
		refuse(w, r, NoCatalogue.New("the Prusa profiles are still being downloaded"))

		return nil
	}

	return m.catalogue
}

// candidate is one preset offered for a spool.
type candidate struct {
	Preset string  `json:"preset"`
	Score  float64 `json:"score"`
	// Source is where the preset comes from.
	Source string `json:"source,omitempty"`
	// Section is the section written for the printer asked about, and Adapted
	// says the preset does not fit it as written: its material settings are
	// laid over a generic preset of the printer.
	Section  string   `json:"section,omitempty"`
	Adapted  bool     `json:"adapted,omitempty"`
	Variants []string `json:"variants"`
}

type bundleBody struct {
	Printer   string    `json:"printer"`
	Filaments []Request `json:"filaments"`
}

// maxSearch bounds the answer of GET /presets, and maxMatches the one of
// GET /match. A page filters them by printer, so more than a few come back.
const (
	maxSearch  = 50
	maxList    = 2000
	maxMatches = 20
)

// describe fills a candidate for a base preset, and for the printer when one
// is named.
func describe(c *Catalogue, base, printer string, score float64, variants map[string][]string) candidate {
	cand := candidate{Preset: base, Score: score, Source: c.BaseSource(base), Variants: nonNil(variants[base])}

	if printer != "" {
		if res, err := c.Resolve(base, printer); err == nil {
			cand.Section, cand.Adapted = res.Section, res.Reference != ""
		}
	}

	return cand
}

// fitFor returns the filter of presets that fit a printer, or nil for none. It
// refuses a printer the bundle does not hold.
func fitFor(w http.ResponseWriter, r *http.Request, c *Catalogue, printer string) (func(string) bool, bool) {
	if printer == "" {
		return nil, true
	}

	if !c.HasPrinter(printer) {
		refuse(w, r, NoPreset.New("no such printer preset: "+printer))

		return nil, false
	}

	return func(base string) bool { return c.Fits(base, printer) }, true
}

func (m *module) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /catalogue", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, m.status())
	})

	mux.HandleFunc("POST /catalogue/refresh", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		if !m.configured {
			m.mu.Unlock()
			refuse(w, r, NotReady.New("the module has not been configured yet"))

			return
		}

		start := !m.refreshing
		m.refreshing = true
		m.mu.Unlock()

		if start {
			go m.refresh()
		}

		answer(w, http.StatusAccepted, m.status())
	})

	// The printer presets of PrusaSlicer, to choose the one a bundle forces.
	mux.HandleFunc("GET /printers", func(w http.ResponseWriter, r *http.Request) {
		c := m.ready(w, r)
		if c == nil {
			return
		}

		answer(w, http.StatusOK, map[string]any{"printers": c.Printers(normalise(r.URL.Query().Get("q")))})
	})

	// The nearest presets for one spool, and for a printer the ones that fit it.
	mux.HandleFunc("GET /match", func(w http.ResponseWriter, r *http.Request) {
		c := m.ready(w, r)
		if c == nil {
			return
		}

		q := r.URL.Query()
		printer := q.Get("printer")

		fit, ok := fitFor(w, r, c, printer)
		if !ok {
			return
		}

		variants := c.Variants()
		out := []candidate{}

		for _, hit := range c.BestMatches(q.Get("brand"), q.Get("name"), q.Get("type"), maxMatches, fit) {
			out = append(out, describe(c, hit.Preset, printer, hit.Score, variants))
		}

		answer(w, http.StatusOK, map[string]any{"matches": out})
	})

	// A search over the base presets, for a spool the match missed.
	mux.HandleFunc("GET /presets", func(w http.ResponseWriter, r *http.Request) {
		c := m.ready(w, r)
		if c == nil {
			return
		}

		q := r.URL.Query()
		printer := q.Get("printer")

		fit, ok := fitFor(w, r, c, printer)
		if !ok {
			return
		}

		words := normalise(q.Get("q"))
		variants := c.Variants()
		bases := c.Bases()

		// A page that lists every preset asks for a long answer without the
		// printer variants it does not read.
		limit, brief := maxSearch, q.Get("brief") == "1"
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			limit = min(n, maxList)
		}

		sort.Strings(bases)

		out := []candidate{}

		for _, base := range bases {
			if !containsAll(strings.Join(normalise(base), " "), words) || (fit != nil && !fit(base)) {
				continue
			}

			if brief {
				out = append(out, candidate{Preset: base, Source: c.BaseSource(base), Variants: []string{}})
			} else {
				out = append(out, describe(c, base, printer, 0, variants))
			}

			if len(out) == limit {
				break
			}
		}

		answer(w, http.StatusOK, map[string]any{"presets": out})
	})

	// The settings a profile would be written with, to edit before the bundle.
	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		c := m.ready(w, r)
		if c == nil {
			return
		}

		var body struct {
			Printer    string      `json:"printer"`
			Base       string      `json:"base"`
			Properties *Properties `json:"properties,omitempty"`
		}
		if !decode(w, r, &body) {
			return
		}

		printer := strings.TrimSpace(body.Printer)
		if printer != "" && !c.HasPrinter(printer) {
			refuse(w, r, NoPreset.New("no such printer preset: "+printer))

			return
		}

		settings, start, err := c.Settings(printer, Request{Base: body.Base, Properties: body.Properties})
		if err != nil {
			refuse(w, r, NoPreset.New(err.Error()))

			return
		}

		answer(w, http.StatusOK, map[string]any{
			"settings": settings,
			"section":  start.source,
			"adapted":  start.adapted,
		})
	})

	mux.HandleFunc("POST /bundle", func(w http.ResponseWriter, r *http.Request) {
		c := m.ready(w, r)
		if c == nil {
			return
		}

		var body bundleBody
		if !decode(w, r, &body) {
			return
		}

		text, err := c.Bundle(strings.TrimSpace(body.Printer), body.Filaments)
		if err != nil {
			if errors.Is(err, errNoPreset) || errors.Is(err, errNoPrinter) {
				refuse(w, r, NoPreset.New(err.Error()))
			} else {
				refuse(w, r, BadRequest.New(err.Error()))
			}

			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="filament-profile.ini"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		refuse(w, r, RouteNotFound.New(r.URL.Path))
	})

	return mux
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}

	return list
}

// containsAll reports whether every word is in the text.
func containsAll(text string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}

	return true
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(into); err != nil {
		refuse(w, r, BadRequest.New("the body is not the JSON this route reads: "+err.Error()))

		return false
	}

	return true
}

func answer(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(body)
}

type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }
