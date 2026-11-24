package main

// A copy of a printer preset whose extruders carry the colours of the spools.
// PrusaSlicer reads a user preset from a bundle without resolving `inherits`:
// every key not in the section takes its default. The copy therefore holds the
// whole printer preset, flattened, with `inherits` naming the system preset
// and `extruder_colour` replaced.

import (
	"fmt"
	"strings"
)

// colouredPrinter writes the section of the copy of a printer preset, and its
// name. colours maps a slot, from 1, to #RRGGBB. A slot with no colour keeps
// the one the printer preset gives its extruder, or none, which follows the
// filament.
func (c *Catalogue) colouredPrinter(printer string, colours map[int]string) (section, name string, err error) {
	keys, vals, err := c.FlattenPrinter(printer)
	if err != nil {
		return "", "", err
	}

	extruders := len(strings.Split(vals["nozzle_diameter"], ","))
	chosen := make([]string, extruders)

	for i, have := range strings.Split(vals["extruder_colour"], ";") {
		if i < extruders {
			chosen[i] = strings.TrimSpace(have)
		}
	}

	for slot, hex := range colours {
		if slot >= 1 && slot <= extruders {
			chosen[slot-1] = hex
		}
	}

	if _, seen := vals["extruder_colour"]; !seen {
		keys = append(keys, "extruder_colour")
	}

	vals["extruder_colour"] = strings.Join(chosen, ";")
	name = presetName(printer) + " (spools)"

	var b strings.Builder

	fmt.Fprintf(&b, "[printer:%s]\ninherits = %s\n", name, printer)

	for _, key := range keys {
		// The id is the application's to set when it loads the preset.
		if key == "inherits" || key == "printer_settings_id" {
			continue
		}

		fmt.Fprintf(&b, "%s = %s\n", key, vals[key])
	}

	return b.String(), name, nil
}
