package main

// The PrusaSlicer 2.x vendor bundle: an INI file whose [filament:NAME]
// sections inherit from each other with `inherits = a; b`. A section whose
// name starts with `*` is abstract: a base for others, never offered to a
// person. This file reads it and flattens one preset, parents first, so the
// result stands alone as a user preset. It uses nothing outside the standard
// library.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Section is one [filament:NAME] of the bundle, keys in file order.
type Section struct {
	Name string
	Keys []string
	Vals map[string]string
}

// Catalogue is the filament sections of one vendor bundle.
type Catalogue struct {
	// Version is the config_version the bundle declares.
	Version  string
	sections map[string]*Section
	order    []string
}

// ParseCatalogue reads a vendor bundle. It keeps the filament sections and the
// vendor's config_version and ignores every other section.
func ParseCatalogue(text string) *Catalogue {
	c := &Catalogue{sections: map[string]*Section{}}

	var (
		current *Section
		vendor  bool
	)

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			head := line[1 : len(line)-1]
			current, vendor = nil, head == "vendor"

			if name, ok := strings.CutPrefix(head, "filament:"); ok {
				current = &Section{Name: name, Vals: map[string]string{}}
				if _, dup := c.sections[name]; !dup {
					c.order = append(c.order, name)
				}

				c.sections[name] = current
			}
		default:
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}

			key, value = strings.TrimSpace(key), strings.TrimSpace(value)

			switch {
			case current != nil:
				if _, seen := current.Vals[key]; !seen {
					current.Keys = append(current.Keys, key)
				}

				current.Vals[key] = value
			case vendor && key == "config_version":
				c.Version = value
			}
		}
	}

	return c
}

// Len is the number of filament sections, abstract ones included.
func (c *Catalogue) Len() int { return len(c.sections) }

// Has reports whether the bundle holds a filament section by this name.
func (c *Catalogue) Has(name string) bool {
	_, ok := c.sections[name]

	return ok
}

// Presets lists the names a person may pick: every section that is not
// abstract, in file order.
func (c *Catalogue) Presets() []string {
	out := make([]string, 0, len(c.order))

	for _, name := range c.order {
		if !strings.HasPrefix(name, "*") {
			out = append(out, name)
		}
	}

	return out
}

// Variants maps each base preset to the suffixes of its printer variants: for
// "Generic PLA" and "Generic PLA @COREONEINDX HF0.4" the suffix is
// "COREONEINDX HF0.4". A base with no variant maps to an empty list.
func (c *Catalogue) Variants() map[string][]string {
	out := map[string][]string{}

	for _, name := range c.Presets() {
		if !strings.Contains(name, " @") {
			out[name] = nil
		}
	}

	for _, name := range c.Presets() {
		base, suffix, ok := strings.Cut(name, " @")
		if !ok {
			continue
		}

		if _, known := out[base]; known {
			out[base] = append(out[base], suffix)
		}
	}

	for _, list := range out {
		sort.Strings(list)
	}

	return out
}

// maxDepth bounds a chain of parents, so a cycle in a bad bundle cannot loop.
const maxDepth = 16

var errNoPreset = errors.New("no such filament preset")

// Flatten returns the keys of a preset with every parent folded in, in the
// order a person reads them: the first parent's keys first, a child's own keys
// last, and a key given twice keeps its last value. The `inherits` key is
// dropped, because the result inherits nothing.
func (c *Catalogue) Flatten(name string) (keys []string, vals map[string]string, err error) {
	vals = map[string]string{}

	if err = c.fold(name, 0, &keys, vals); err != nil {
		return nil, nil, err
	}

	return keys, vals, nil
}

func (c *Catalogue) fold(name string, depth int, keys *[]string, vals map[string]string) error {
	if depth > maxDepth {
		return fmt.Errorf("%q inherits too deeply", name)
	}

	section, ok := c.sections[name]
	if !ok {
		return fmt.Errorf("%w: %q", errNoPreset, name)
	}

	for _, parent := range strings.Split(section.Vals["inherits"], ";") {
		if parent = strings.TrimSpace(parent); parent == "" {
			continue
		}

		if err := c.fold(parent, depth+1, keys, vals); err != nil {
			return err
		}
	}

	for _, key := range section.Keys {
		if key == "inherits" {
			continue
		}

		if _, seen := vals[key]; !seen {
			*keys = append(*keys, key)
		}

		vals[key] = section.Vals[key]
	}

	return nil
}

// normalise lowercases a name and keeps letters and digits, so "PolyLite™ PLA"
// and "polylite pla" compare equal.
func normalise(s string) []string {
	var b strings.Builder

	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+':
			b.WriteString("plus")
		default:
			b.WriteByte(' ')
		}
	}

	return strings.Fields(b.String())
}

// Match is one preset a filament may be built from.
type Match struct {
	Preset string  `json:"preset"`
	Score  float64 `json:"score"`
}

// BestMatches ranks the base presets against a spool's brand, name and type.
// A preset scores the share of its own words found in "brand name"; the brand
// is worth a word of its own, so "Prusament PLA" beats "Generic PLA" for a
// Prusament spool. When the spool names a brand, only that brand's presets
// match. The generic preset of the type always closes the list when
// it exists, so a person can build a profile for a brand the bundle does not
// know. At most limit entries come back.
func (c *Catalogue) BestMatches(brand, name, kind string, limit int) []Match {
	have := map[string]bool{}
	for _, w := range normalise(brand + " " + name) {
		have[w] = true
	}

	brands := map[string]bool{}
	for _, w := range normalise(brand) {
		brands[w] = true
	}

	var out []Match

	for _, preset := range c.Presets() {
		if strings.Contains(preset, " @") || strings.HasPrefix(preset, "Generic ") {
			continue
		}

		words := normalise(preset)
		if len(words) == 0 {
			continue
		}

		// A preset is a brand's: its first word names the brand, and it must be
		// the spool's. "Filatech Wood-PLA" is not a Geeetech Wood PLA.
		if len(brands) > 0 && !brands[words[0]] {
			continue
		}

		hit := 0

		for _, w := range words {
			if have[w] {
				hit++
			}
		}

		// A preset must share more than the material word to be a match.
		if hit < 2 || hit*2 < len(words) {
			continue
		}

		out = append(out, Match{Preset: preset, Score: float64(hit) / float64(len(words))})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}

		return len(out[i].Preset) < len(out[j].Preset)
	})

	if limit > 1 && len(out) > limit-1 {
		out = out[:limit-1]
	}

	if kind = strings.TrimSpace(kind); kind != "" {
		if generic := "Generic " + strings.ToUpper(kind); c.Has(generic) {
			out = append(out, Match{Preset: generic})
		}
	}

	return out
}
