package main

// Writing the PrusaSlicer 2.9 Config Bundle: one [filament:NAME] section per
// spool, each flattened so it imports without its parents. PrusaSlicer reads
// it from File > Import > Import Config Bundle. Its [presets] section selects
// the printer and the filament of each extruder.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Request is one filament profile to write.
type Request struct {
	// Name is what the profile is called in PrusaSlicer, without the variant.
	Name string `json:"name"`
	// Vendor is the brand, written to filament_vendor. Empty keeps the base's.
	Vendor string `json:"vendor,omitempty"`
	// Base is the preset the settings are taken from.
	Base string `json:"base"`
	// Variant is the printer suffix of the base, such as "COREONEINDX HF0.4".
	// It is read only when the bundle names no printer, which picks the
	// section that fits instead. Empty takes the base preset itself.
	Variant string `json:"variant,omitempty"`
	// ColorRGBA is the spool's colour as #rrggbb or #rrggbbaa. Empty keeps the base's.
	ColorRGBA string `json:"color_rgba,omitempty"`
	// Slot is the filament position the profile takes in PrusaSlicer, from 1:
	// slot 1 is the first extruder's filament, slot 2 the second's. The
	// bundle's [presets] section selects each profile in its slot when the
	// bundle is imported. Zero leaves the selection alone.
	Slot int `json:"slot,omitempty"`
	// Properties are the maker's advised temperatures and the density, from
	// the OpenPrintTag catalogue. They replace the base's own. Absent keeps them.
	Properties *Properties `json:"properties,omitempty"`
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

// materialKeys are the settings that belong to a material, and not to a
// printer. A preset of another source is adapted to a printer by laying these
// over a generic preset that fits it: the start and end G-code, the loading
// and ramming values and the compatibility stay the printer's.
var materialKeys = []string{
	"filament_type", "filament_soluble", "filament_abrasive", "filament_density", "filament_cost",
	"filament_diameter", "filament_spool_weight", "extrusion_multiplier",
	"temperature", "first_layer_temperature", "bed_temperature", "first_layer_bed_temperature",
	"cooling", "fan_always_on", "fan_below_layer_time", "bridge_fan_speed", "disable_fan_first_layers",
	"full_fan_speed_layer", "min_fan_speed", "max_fan_speed", "enable_dynamic_fan_speeds",
	"overhang_fan_speed_0", "overhang_fan_speed_1", "overhang_fan_speed_2", "overhang_fan_speed_3",
	"slowdown_below_layer_time", "min_print_speed",
	"filament_retract_length", "filament_retract_speed", "filament_retract_lift",
	"filament_retract_lift_above", "filament_retract_lift_below", "filament_retract_before_travel",
	"filament_retract_before_wipe", "filament_retract_layer_change", "filament_retract_restart_extra",
	"filament_deretract_speed", "filament_wipe",
}

// volumetric is the one material key a printer also limits: a preset tuned for
// another nozzle must not ask more of this one than a generic preset allows.
const volumetric = "filament_max_volumetric_speed"

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

// resolved is what a request starts from.
type resolved struct {
	keys []string
	vals map[string]string
	// named is the section whose printer variant names the profile.
	named string
	// source is the preset the settings come from, and adapted the generic
	// preset they were laid over when the source's did not fit the printer.
	source, adapted string
}

// start returns the settings a request starts from. With a printer, the
// section is the one that fits it. Without one, it is the base and its Variant.
func (c *Catalogue) start(printer string, req Request) (resolved, error) {
	if printer == "" {
		source := req.Base
		if v := strings.TrimSpace(req.Variant); v != "" {
			source += " @" + v
		}

		keys, vals, err := c.Flatten(source)

		return resolved{keys: keys, vals: vals, named: source, source: source}, err
	}

	res, err := c.Resolve(req.Base, printer)
	if err != nil {
		return resolved{}, err
	}

	if res.Section == "" {
		return resolved{}, fmt.Errorf("%w: %q has no preset for %q", errNoPreset, req.Base, printer)
	}

	if res.Reference == "" {
		keys, vals, err := c.Flatten(res.Section)

		return resolved{keys: keys, vals: vals, named: res.Section, source: res.Section}, err
	}

	// The preset is of another source and does not fit as written: take a
	// generic preset that fits, and lay the material's settings over it.
	keys, vals, err := c.Flatten(res.Reference)
	if err != nil {
		return resolved{}, err
	}

	_, own, err := c.Flatten(res.Section)
	if err != nil {
		return resolved{}, err
	}

	set := func(key, value string) {
		if _, seen := vals[key]; !seen {
			keys = append(keys, key)
		}

		vals[key] = value
	}

	for _, key := range materialKeys {
		if value, ok := own[key]; ok {
			set(key, value)
		}
	}

	// A flow the source tuned for another nozzle is kept only when it asks
	// less of this one than the generic preset does.
	mine, errMine := strconv.ParseFloat(strings.TrimSpace(own[volumetric]), 64)
	limit, errLimit := strconv.ParseFloat(strings.TrimSpace(vals[volumetric]), 64)

	if errMine == nil && (errLimit != nil || mine < limit) {
		set(volumetric, strings.TrimSpace(own[volumetric]))
	}

	return resolved{keys: keys, vals: vals, named: res.Reference, source: res.Section, adapted: res.Reference}, nil
}

// Bundle writes the Config Bundle for the requests. With a printer, it also
// selects that printer when the bundle is imported, and every profile is the
// section of its preset that fits it.
func (c *Catalogue) Bundle(printer string, requests []Request) (string, error) {
	switch {
	case len(requests) == 0:
		return "", errNoRequest
	case len(requests) > MaxRequests:
		return "", fmt.Errorf("at most %d filaments in one bundle", MaxRequests)
	case printer != "" && !c.HasPrinter(printer):
		return "", fmt.Errorf("%w: %q", errNoPrinter, printer)
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

		if req.Slot < 0 || req.Slot > MaxRequests {
			return "", fmt.Errorf("%w: %d", errSlot, req.Slot)
		}

		start, err := c.start(printer, req)
		if err != nil {
			return "", err
		}

		keys, vals := start.keys, start.vals

		// The printer variant of the section goes after the name, as Prusa's
		// own presets do, so two printers do not share a profile.
		if _, variant, ok := strings.Cut(start.named, " @"); ok {
			name += " @" + presetName(variant)
		}

		if used[name] {
			return "", fmt.Errorf("two profiles are named %q", name)
		}

		used[name] = true

		if req.Slot > 0 {
			if other, taken := selected[req.Slot]; taken {
				return "", fmt.Errorf("%q and %q both take slot %d", other, name, req.Slot)
			}

			selected[req.Slot] = name
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

		note := "Written by filament-profile from " + strings.ReplaceAll(presetName(start.source), `"`, "")
		if start.adapted != "" {
			note += ", adapted to " + strings.ReplaceAll(presetName(start.adapted), `"`, "") + " for this printer"
		}

		note += " (Prusa Research bundle " + c.Version + ")"

		if req.Properties.apply(vals, set) {
			note += "; temperatures and density from the OpenPrintTag catalogue"
		}

		set("filament_notes", `"`+note+`"`)

		fmt.Fprintf(&out, "\n[filament:%s]\n", name)

		for _, key := range keys {
			if dropped[key] {
				continue
			}

			// The catalogue's own values are single lines already.
			fmt.Fprintf(&out, "%s = %s\n", key, vals[key])
		}
	}

	if printer != "" || len(selected) > 0 {
		out.WriteString("\n[presets]\n")

		if printer != "" {
			fmt.Fprintf(&out, "printer = %s\n", printer)
		}

		slots := make([]int, 0, len(selected))
		for slot := range selected {
			slots = append(slots, slot)
		}

		sort.Ints(slots)

		// PrusaSlicer keeps the filament of extruder 1 under `filament` and the
		// next ones under `filament_1`, `filament_2`, and so on.
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
