package main

// The sources of filament presets. Prusa Research's bundle is always loaded: it
// holds the printers and the generic presets the others are adapted on. Other
// sources add presets of their own, and the operator chooses which are offered
// with the `sources` setting:
//
//	prusa      the presets Prusa Research publishes for PrusaSlicer
//	polymaker  Polymaker's own presets (github.com/Polymaker3D/Polymaker-Preset, MIT)
//	custom     PrusaSlicer config bundles at the addresses of `custom_urls`

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	sourcePrusa     = "prusa"
	sourcePolymaker = "polymaker"
	sourceCustom    = "custom"

	defaultCatalogueURL = "https://storage.googleapis.com/prusa3d-content-prod-14e8-preset-repo-api-public/prusa-fff/prusa-fff-offline.zip"
	defaultPolymakerURL = "https://github.com/Polymaker3D/Polymaker-Preset/archive/refs/heads/main.zip"

	// maxPreset bounds one preset file read from an archive, and maxPresets the
	// number of them: a source has a few hundred.
	maxPreset  = 1 << 20
	maxPresets = 4000

	// maxCustom bounds the custom bundles an operator may name.
	maxCustom = 8
)

// polymakerFamily is the printers Polymaker's presets may be adapted to. They
// are written for the CORE One: its family shares the extruder, and the
// generic preset of the printer chosen supplies the rest.
var polymakerFamily = regexp.MustCompile(`^COREONE`)

// knownSources are the ids the `sources` setting accepts.
var knownSources = []string{sourcePrusa, sourcePolymaker, sourceCustom}

// parseSources reads the `sources` setting: ids separated by commas, in any
// case. An id it does not know is dropped, and an empty list means all of
// Prusa and Polymaker.
func parseSources(list string) []string {
	var out []string

	seen := map[string]bool{}

	for _, id := range strings.Split(list, ",") {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || seen[id] {
			continue
		}

		for _, known := range knownSources {
			if id == known {
				seen[id] = true
				out = append(out, id)
			}
		}
	}

	if len(out) == 0 {
		return []string{sourcePrusa, sourcePolymaker}
	}

	return out
}

// enabled says whether a source is in the list the operator wrote.
func enabled(ids []string, id string) bool {
	for _, have := range ids {
		if have == id {
			return true
		}
	}

	return false
}

// customURLs reads the `custom_urls` setting: addresses separated by commas
// or spaces. Only https addresses are kept, at most maxCustom.
func customURLs(list string) []string {
	var out []string

	for _, field := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if strings.HasPrefix(field, "https://") && len(out) < maxCustom {
			out = append(out, field)
		}
	}

	return out
}

// sourceFile is where a source's bundle is kept in the data directory.
func sourceFile(id string, index int) string {
	if id == sourceCustom {
		return fmt.Sprintf("source-%s-%d.ini", id, index)
	}

	return "source-" + id + ".ini"
}

var configLine = regexp.MustCompile(`^[a-z][a-z0-9_]* =`)

// polymakerBundle turns the repository archive of Polymaker's presets into one
// config bundle. Each PrusaSlicer preset is a file of settings, saved at
// preset/<material>/<vendor>/<printer>/PrusaSlicer/<name>.ini, and becomes the
// section [filament:<name>].
func polymakerBundle(archive []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return "", fmt.Errorf("read the Polymaker archive: %w", err)
	}

	type preset struct{ name, body string }

	var presets []preset

	for _, f := range zr.File {
		parts := strings.Split(f.Name, "/")
		if len(parts) != 7 || parts[1] != "preset" || parts[5] != "PrusaSlicer" || !strings.HasSuffix(parts[6], ".ini") {
			continue
		}

		name := strings.TrimSuffix(parts[6], ".ini")
		if name == "" || strings.ContainsAny(name, "[]\r\n") || f.UncompressedSize64 > maxPreset {
			continue
		}

		if len(presets) == maxPresets {
			break
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}

		raw, err := io.ReadAll(io.LimitReader(rc, maxPreset+1))

		rc.Close()

		if err != nil || len(raw) > maxPreset {
			continue
		}

		var body strings.Builder

		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(line); configLine.MatchString(line) {
				body.WriteString(line + "\n")
			}
		}

		presets = append(presets, preset{name: name, body: body.String()})
	}

	if len(presets) == 0 {
		return "", errors.New("the Polymaker archive holds no PrusaSlicer preset")
	}

	sort.Slice(presets, func(i, j int) bool { return presets[i].name < presets[j].name })

	var out strings.Builder

	for _, p := range presets {
		fmt.Fprintf(&out, "[filament:%s]\n%s\n", p.name, p.body)
	}

	return out.String(), nil
}

// fetchSources downloads the sources the settings enable, keeps each bundle in
// the data directory, and returns what went wrong with any of them. The bundle
// of Prusa Research is the one whose failure matters: without it there is no
// printer to choose.
func fetchSources(ctx context.Context, client *http.Client, dir string, st Settings) []string {
	var problems []string

	fail := func(id string, err error) { problems = append(problems, fmt.Sprintf("%s: %v", id, err)) }

	archive, err := download(ctx, client, st.CatalogueURL)
	if err == nil {
		var bundle []byte
		if bundle, err = newestBundle(archive); err == nil {
			err = saveBundle(dir, bundleFile, bundle)
		}
	}

	if err != nil {
		fail(sourcePrusa, err)
	}

	ids := parseSources(st.Sources)

	if enabled(ids, sourcePolymaker) {
		var text string

		archive, err = download(ctx, client, st.PolymakerURL)
		if err == nil {
			text, err = polymakerBundle(archive)
		}

		if err == nil {
			err = saveBundle(dir, sourceFile(sourcePolymaker, 0), []byte(text))
		}

		if err != nil {
			fail(sourcePolymaker, err)
		}
	}

	if enabled(ids, sourceCustom) {
		for i, address := range customURLs(st.CustomURLs) {
			data, err := download(ctx, client, address)
			if err == nil {
				err = saveBundle(dir, sourceFile(sourceCustom, i), data)
			}

			if err != nil {
				fail(fmt.Sprintf("%s %d", sourceCustom, i+1), err)
			}
		}
	}

	return problems
}

// assemble builds the catalogue from the bundles kept in the data directory.
func assemble(dir string, st Settings) (*Catalogue, error) {
	prusa, err := os.ReadFile(filepath.Join(dir, bundleFile))
	if err != nil {
		return nil, fmt.Errorf("read the Prusa bundle: %w", err)
	}

	c := ParseCatalogue(string(prusa))
	if c.Len() == 0 {
		return nil, errors.New("the Prusa bundle holds no filament preset")
	}

	ids := parseSources(st.Sources)

	if enabled(ids, sourcePolymaker) {
		if data, err := os.ReadFile(filepath.Join(dir, sourceFile(sourcePolymaker, 0))); err == nil {
			c.AddSource(sourcePolymaker, "polymaker", polymakerFamily, string(data))
		}
	}

	if enabled(ids, sourceCustom) {
		for i := range customURLs(st.CustomURLs) {
			if data, err := os.ReadFile(filepath.Join(dir, sourceFile(sourceCustom, i))); err == nil {
				c.AddSource(sourceCustom, "", nil, string(data))
			}
		}
	}

	c.SetSources(ids)

	return c, nil
}

// stale reports whether a source file is missing or older than the refresh
// period, which asks for a download.
func stale(dir string, st Settings) bool {
	old := func(name string) bool {
		info, err := os.Stat(filepath.Join(dir, name))

		return err != nil || time.Since(info.ModTime()) > time.Duration(st.RefreshDays)*24*time.Hour
	}

	if old(bundleFile) {
		return true
	}

	ids := parseSources(st.Sources)

	if enabled(ids, sourcePolymaker) && old(sourceFile(sourcePolymaker, 0)) {
		return true
	}

	if enabled(ids, sourceCustom) {
		for i := range customURLs(st.CustomURLs) {
			if old(sourceFile(sourceCustom, i)) {
				return true
			}
		}
	}

	return false
}
