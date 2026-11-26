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
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Section is one [filament:NAME] of the bundle, keys in file order.
type Section struct {
	Name string
	Keys []string
	Vals map[string]string
	// Source says where the section comes from: "prusa" for the bundle Prusa
	// Research publishes, or the id of another source.
	Source string
	// Brand is the maker the source gives to all its presets, such as
	// "polymaker", for a source whose preset names do not start with it.
	Brand string
}

// Catalogue is the filament sections of one vendor bundle.
type Catalogue struct {
	// Version is the config_version the bundle declares.
	Version  string
	sections map[string]*Section
	order    []string

	// The printer presets, which say what a filament is compatible with.
	printers     map[string]*Section
	printerOrder []string
	// byBase lists, for each base preset, itself and its printer variants.
	byBase map[string][]string

	// enabled limits the presets offered to these sources. Nil offers all.
	enabled map[string]bool
	// retarget names, for a source, the printers (by model) its presets may be
	// adapted to when none fits as written.
	retarget map[string]*regexp.Regexp

	// cache keeps, for a few printers, the filament sections that fit them.
	mu    sync.Mutex
	cache map[string]map[string]bool
}

// ParseCatalogue reads a vendor bundle. It keeps the filament sections and the
// vendor's config_version and ignores every other section.
func ParseCatalogue(text string) *Catalogue {
	c := &Catalogue{
		sections: map[string]*Section{},
		printers: map[string]*Section{},
		cache:    map[string]map[string]bool{},
		retarget: map[string]*regexp.Regexp{},
	}

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

			if name, ok := strings.CutPrefix(head, "printer:"); ok {
				current = &Section{Name: name, Vals: map[string]string{}}
				if _, dup := c.printers[name]; !dup {
					c.printerOrder = append(c.printerOrder, name)
				}

				c.printers[name] = current
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

	for _, section := range c.sections {
		section.Source = "prusa"
	}

	c.reindex()

	return c
}

// reindex rebuilds the list of sections of each base preset.
func (c *Catalogue) reindex() {
	c.byBase = map[string][]string{}

	for _, name := range c.order {
		if strings.HasPrefix(name, "*") {
			continue
		}

		base, _, _ := strings.Cut(name, " @")
		c.byBase[base] = append(c.byBase[base], name)
	}
}

// AddSource merges the filament sections of another bundle into the
// catalogue, under a source id. A section whose name the catalogue already
// holds is left alone: the first source to name a preset keeps it. brand is the
// maker the source gives to all its presets, and retarget, when it is not nil,
// says by printer model which printers they may be adapted to. It returns the
// number of presets added.
func (c *Catalogue) AddSource(id, brand string, retarget *regexp.Regexp, bundle string) int {
	other := ParseCatalogue(bundle)
	added := 0

	for _, name := range other.order {
		if _, dup := c.sections[name]; dup {
			continue
		}

		section := other.sections[name]
		section.Source, section.Brand = id, brand
		c.sections[name] = section
		c.order = append(c.order, name)

		if !strings.HasPrefix(name, "*") {
			added++
		}
	}

	if retarget != nil {
		c.retarget[id] = retarget
	}

	c.mu.Lock()
	c.cache = map[string]map[string]bool{}
	c.mu.Unlock()
	c.reindex()

	return added
}

// SetSources limits the presets offered to these sources. The bundle of Prusa
// Research stays loaded whatever is chosen: it holds the printers and the
// generic presets the others are adapted on.
func (c *Catalogue) SetSources(ids []string) {
	c.enabled = map[string]bool{}
	for _, id := range ids {
		c.enabled[id] = true
	}
}

// visible reports whether a source is one the operator chose to offer.
func (c *Catalogue) visible(name string) bool {
	if c.enabled == nil {
		return true
	}

	section, ok := c.sections[name]

	return ok && c.enabled[section.Source]
}

// SourceOf names the source of a filament section.
func (c *Catalogue) SourceOf(name string) string {
	if section, ok := c.sections[name]; ok {
		return section.Source
	}

	return ""
}

// Counts returns, for each source, the number of presets it gave.
func (c *Catalogue) Counts() map[string]int {
	out := map[string]int{}

	for _, name := range c.order {
		if !strings.HasPrefix(name, "*") {
			out[c.sections[name].Source]++
		}
	}

	return out
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
		if !strings.HasPrefix(name, "*") && c.visible(name) {
			out = append(out, name)
		}
	}

	return out
}

// Bases lists the base presets a person may pick, in file order: the name of
// each visible preset without its printer variant. A preset a source publishes
// only for printers has no section of its own: "Panchroma PLA @Prusa Core One"
// gives the base "Panchroma PLA".
func (c *Catalogue) Bases() []string {
	seen := map[string]bool{}
	out := []string{}

	for _, name := range c.Presets() {
		base, _, _ := strings.Cut(name, " @")
		if !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}

	return out
}

// brandOf is the maker a source gives to a base preset, in the words of
// normalise, or "" when the source gives none.
func (c *Catalogue) brandOf(base string) string {
	if names := c.byBase[base]; len(names) > 0 {
		return c.sections[names[0]].Brand
	}

	return ""
}

// hasBase reports whether a visible base preset has this name.
func (c *Catalogue) hasBase(base string) bool {
	for _, name := range c.byBase[base] {
		if c.visible(name) {
			return true
		}
	}

	return false
}

// BaseSource names the source of a base preset.
func (c *Catalogue) BaseSource(base string) string {
	if names := c.byBase[base]; len(names) > 0 {
		return c.sections[names[0]].Source
	}

	return ""
}

// Variants maps each base preset to the suffixes of its printer variants: for
// "Generic PLA" and "Generic PLA @COREONEINDX HF0.4" the suffix is
// "COREONEINDX HF0.4". A base with no variant maps to an empty list.
func (c *Catalogue) Variants() map[string][]string {
	out := map[string][]string{}

	for _, base := range c.Bases() {
		out[base] = nil
	}

	for _, name := range c.Presets() {
		if base, suffix, ok := strings.Cut(name, " @"); ok {
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

	if err = c.fold(c.sections, name, 0, &keys, vals); err != nil {
		return nil, nil, err
	}

	return keys, vals, nil
}

// fold flattens one preset: it first takes the keys of every preset it inherits
// from (the parents named in `inherits`, left to right), then its own, which
// win. `depth` stops a loop of inheritance from running forever.
func (c *Catalogue) fold(sections map[string]*Section, name string, depth int, keys *[]string, vals map[string]string) error {
	if depth > maxDepth {
		return fmt.Errorf("%q inherits too deeply", name)
	}

	section, ok := sections[name]
	if !ok {
		return fmt.Errorf("%w: %q", errNoPreset, name)
	}

	for _, parent := range strings.Split(section.Vals["inherits"], ";") {
		if parent = strings.TrimSpace(parent); parent == "" {
			continue
		}

		if err := c.fold(sections, parent, depth+1, keys, vals); err != nil {
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

// typeOf is the material type of a base preset, such as "PLA", or "" when its
// settings do not say.
func (c *Catalogue) typeOf(base string) string {
	section := c.anySection(base)
	if section == "" {
		return ""
	}

	_, vals, err := c.Flatten(section)
	if err != nil {
		return ""
	}

	return strings.ToUpper(strings.TrimSpace(vals["filament_type"]))
}

// BestMatches ranks the base presets against a spool's brand, name and type.
// A preset scores the share of its own words found in "brand name"; the brand
// is worth a word of its own, so "Prusament PLA" beats "Generic PLA" for a
// Prusament spool. When the spool names a brand, only that brand's presets
// match. The generic preset of the type always closes the list when
// it exists, so a person can build a profile for a brand the bundle does not
// know. At most limit entries come back.
func (c *Catalogue) BestMatches(brand, name, kind string, limit int, fit func(preset string) bool) []Match {
	have := map[string]bool{}
	for _, w := range normalise(brand + " " + name) {
		have[w] = true
	}

	brands := map[string]bool{}
	for _, w := range normalise(brand) {
		brands[w] = true
	}

	var out []Match

	for _, preset := range c.Bases() {
		if strings.HasPrefix(preset, "Generic ") {
			continue
		}

		words := normalise(preset)
		if len(words) == 0 {
			continue
		}

		// A preset is a brand's: its first word names the brand, and it must be
		// the spool's. "Filatech Wood-PLA" is not a Geeetech Wood PLA.
		if len(brands) > 0 && !brands[words[0]] && !brands[c.brandOf(preset)] {
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

		if fit != nil && !fit(preset) {
			continue
		}

		// A PETG preset is not a match for a PLA spool.
		if want := strings.ToUpper(strings.TrimSpace(kind)); want != "" {
			if have := c.typeOf(preset); have != "" && have != want {
				continue
			}
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
		if generic := "Generic " + strings.ToUpper(kind); c.hasBase(generic) && (fit == nil || fit(generic)) {
			out = append(out, Match{Preset: generic})
		}
	}

	return out
}
