// Command filament-profile is a Prusa Connect module that writes PrusaSlicer
// 2.9 filament profiles for the spools loaded on a printer's tools. It reads
// the profiles Prusa Research publishes, finds the nearest preset for each
// spool, and writes one Config Bundle (.ini) a person imports into
// PrusaSlicer.
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
	release = extension.Version("0.3.0")

	defaultCatalogueURL = "https://storage.googleapis.com/prusa3d-content-prod-14e8-preset-repo-api-public/prusa-fff/prusa-fff-offline.zip"
)

// Settings is what an operator writes under this instance's config block. The
// defaults live in module.yaml.
type Settings struct {
	CatalogueURL string `json:"catalogue_url"`
	RefreshDays  int    `json:"refresh_days"`
}

func (s Settings) withDefaults() Settings {
	if strings.TrimSpace(s.CatalogueURL) == "" {
		s.CatalogueURL = defaultCatalogueURL
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

	m := &module{ctx: ctx, client: &http.Client{Timeout: 2 * time.Minute}}

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

// configure reads the catalogue kept in the data directory, and downloads a
// fresh one when there is none or it is older than refresh_days.
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

	stale := true

	if data, err := os.ReadFile(filepath.Join(grants.Data, bundleFile)); err == nil {
		if c := ParseCatalogue(string(data)); c.Len() > 0 {
			m.catalogue = c

			if info, err := os.Stat(filepath.Join(grants.Data, bundleFile)); err == nil {
				m.updated = info.ModTime()
				stale = time.Since(info.ModTime()) > time.Duration(settings.RefreshDays)*24*time.Hour
			}
		}
	}

	if stale {
		m.refreshing = true

		go m.refresh()
	}

	return nil
}

// refresh downloads the catalogue and swaps it in. Callers set m.refreshing
// first, so two downloads never run together.
func (m *module) refresh() {
	ctx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
	defer cancel()

	c, data, err := m.fetchCatalogue(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.refreshing = false

	if err != nil {
		m.lastError = err.Error()
		logf("catalogue refresh failed: %v", err)

		return
	}

	if err = saveBundle(m.dir, data); err != nil {
		m.lastError = err.Error()
		logf("%v", err)

		return
	}

	m.catalogue, m.updated, m.lastError = c, time.Now(), ""
	logf("catalogue %s loaded: %d filament sections", c.Version, c.Len())
}

func (m *module) fetchCatalogue(ctx context.Context) (*Catalogue, []byte, error) {
	archive, err := download(ctx, m.client, m.settings.CatalogueURL)
	if err != nil {
		return nil, nil, err
	}

	data, err := newestBundle(archive)
	if err != nil {
		return nil, nil, err
	}

	c := ParseCatalogue(string(data))
	if c.Len() == 0 {
		return nil, nil, errors.New("the bundle holds no filament preset")
	}

	return c, data, nil
}

// status is the answer of GET /catalogue.
type status struct {
	Ready      bool       `json:"ready"`
	Refreshing bool       `json:"refreshing"`
	Version    string     `json:"version,omitempty"`
	Presets    int        `json:"presets"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func (m *module) status() status {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := status{Refreshing: m.refreshing, Error: m.lastError}
	if m.catalogue != nil {
		s.Ready, s.Version, s.Presets = true, m.catalogue.Version, len(m.catalogue.Presets())
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

// candidate is one preset offered for a spool, with the printer variants it has.
type candidate struct {
	Preset   string   `json:"preset"`
	Score    float64  `json:"score"`
	Variants []string `json:"variants"`
}

type bundleBody struct {
	Filaments []Request `json:"filaments"`
}

// maxSearch bounds the answer of GET /presets, and maxMatches the one of
// GET /match. A page filters them by printer, so more than a few come back.
const (
	maxSearch  = 50
	maxMatches = 20
)

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

	// The nearest presets for one spool, and the printer variants of each.
	mux.HandleFunc("GET /match", func(w http.ResponseWriter, r *http.Request) {
		c := m.ready(w, r)
		if c == nil {
			return
		}

		q := r.URL.Query()
		variants := c.Variants()
		out := []candidate{}

		for _, hit := range c.BestMatches(q.Get("brand"), q.Get("name"), q.Get("type"), maxMatches) {
			out = append(out, candidate{Preset: hit.Preset, Score: hit.Score, Variants: nonNil(variants[hit.Preset])})
		}

		answer(w, http.StatusOK, map[string]any{"matches": out})
	})

	// A search over the base presets, for a spool the match missed.
	mux.HandleFunc("GET /presets", func(w http.ResponseWriter, r *http.Request) {
		c := m.ready(w, r)
		if c == nil {
			return
		}

		words := normalise(r.URL.Query().Get("q"))
		variants := c.Variants()
		bases := make([]string, 0, len(variants))

		for base := range variants {
			bases = append(bases, base)
		}

		sort.Strings(bases)

		out := []candidate{}

		for _, base := range bases {
			have := strings.Join(normalise(base), " ")
			if !containsAll(have, words) {
				continue
			}

			out = append(out, candidate{Preset: base, Variants: nonNil(variants[base])})
			if len(out) == maxSearch {
				break
			}
		}

		answer(w, http.StatusOK, map[string]any{"presets": out})
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

		text, err := c.Bundle(body.Filaments)
		if err != nil {
			if errors.Is(err, errNoPreset) {
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
