package main

// Writing the PrusaSlicer 2.9 Config Bundle: one [filament:NAME] section per
// spool, each flattened so it imports without its parents. PrusaSlicer reads
// it from File > Import > Import Config Bundle.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Request is one filament profile to write.
type Request struct {
	// Name is what the profile is called in PrusaSlicer, without the variant.
	Name string `json:"name"`
	// Vendor is the brand, written to filament_vendor. Empty keeps the base's.
	Vendor string `json:"vendor,omitempty"`
	// Base is the preset of the bundle the settings are taken from.
	Base string `json:"base"`
	// Variant is the printer suffix of the base, such as "COREONEINDX HF0.4".
	// Empty takes the base preset itself.
	Variant string `json:"variant,omitempty"`
	// ColorRGBA is the spool's colour as #rrggbb or #rrggbbaa. Empty keeps the base's.
	ColorRGBA string `json:"color_rgba,omitempty"`
	// Slot is the filament position the profile takes in PrusaSlicer, from 1:
	// slot 1 is the first extruder's filament, slot 2 the second's. The
	// bundle's [presets] section selects each profile in its slot when the
	// bundle is imported. Zero leaves the selection alone.
	Slot int `json:"slot,omitempty"`
}

// MaxRequests bounds one bundle: a printer has few tools.
const MaxRequests = 32

var (
	errNoRequest = errors.New("no filament to write")
	errName      = errors.New("a profile needs a name")
	errSlot      = errors.New("a slot is a number from 1 to the size of the bundle")
)

// dropped are keys of a parent that must not reach a user preset: an alias or
// a former name would shadow the preset it was copied from.
var dropped = map[string]bool{"inherits": true, "alias": true, "renamed_from": true}

// presetName makes a name PrusaSlicer can keep as a file name.
func presetName(s string) string {
	repl := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "-",
		"[", "(", "]", ")", "\r", " ", "\n", " ", "™", "", "®", "",
	)

	return strings.Join(strings.Fields(repl.Replace(s)), " ")
}

// oneLine keeps a value on its line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// colour turns #rrggbb or #rrggbbaa into the #RRGGBB PrusaSlicer reads, and
// reports false for anything else.
func colour(rgba string) (string, bool) {
	hex, ok := strings.CutPrefix(strings.TrimSpace(rgba), "#")
	if !ok || (len(hex) != 6 && len(hex) != 8) {
		return "", false
	}

	for _, r := range hex {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", false
		}
	}

	return "#" + strings.ToUpper(hex[:6]), true
}

// Bundle writes the Config Bundle for the requests.
func (c *Catalogue) Bundle(requests []Request) (string, error) {
	switch {
	case len(requests) == 0:
		return "", errNoRequest
	case len(requests) > MaxRequests:
		return "", fmt.Errorf("at most %d filaments in one bundle", MaxRequests)
	}

	var out strings.Builder

	out.WriteString("# Filament profiles for PrusaSlicer 2.9, written by filament-profile.\n")
	out.WriteString("# Import with File > Import > Import Config Bundle.\n")

	used := map[string]bool{}
	selected := map[int]string{}

	for _, req := range requests {
		name := presetName(req.Name)
		if name == "" {
			return "", errName
		}

		source := req.Base
		if v := strings.TrimSpace(req.Variant); v != "" {
			source += " @" + v
			name += " @" + presetName(v)
		}

		if used[name] {
			return "", fmt.Errorf("two profiles are named %q", name)
		}

		used[name] = true

		if req.Slot < 0 || req.Slot > MaxRequests {
			return "", fmt.Errorf("%w: %d", errSlot, req.Slot)
		}

		if req.Slot > 0 {
			if other, taken := selected[req.Slot]; taken {
				return "", fmt.Errorf("%q and %q both take slot %d", other, name, req.Slot)
			}

			selected[req.Slot] = name
		}

		keys, vals, err := c.Flatten(source)
		if err != nil {
			return "", err
		}

		set := func(key, value string) {
			if _, seen := vals[key]; !seen {
				keys = append(keys, key)
			}

			vals[key] = value
		}

		if v := oneLine(req.Vendor); v != "" {
			set("filament_vendor", v)
		}

		if hex, ok := colour(req.ColorRGBA); ok {
			set("filament_colour", hex)
		}

		set("filament_notes", `"Written by filament-profile from `+strings.ReplaceAll(presetName(source), `"`, "")+` (Prusa Research bundle `+c.Version+`)"`)

		fmt.Fprintf(&out, "\n[filament:%s]\n", name)

		for _, key := range keys {
			if dropped[key] {
				continue
			}

			// The catalogue's own values are single lines already.
			fmt.Fprintf(&out, "%s = %s\n", key, vals[key])
		}
	}

	if len(selected) > 0 {
		slots := make([]int, 0, len(selected))
		for slot := range selected {
			slots = append(slots, slot)
		}

		sort.Ints(slots)

		// PrusaSlicer keeps the filament of extruder 1 under `filament` and the
		// next ones under `filament_1`, `filament_2`, and so on.
		out.WriteString("\n[presets]\n")

		for _, slot := range slots {
			key := "filament"
			if slot > 1 {
				key = fmt.Sprintf("filament_%d", slot-1)
			}

			fmt.Fprintf(&out, "%s = %s\n", key, selected[slot])
		}
	}

	return out.String(), nil
}
