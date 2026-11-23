package main

// The measured properties of a spool's material, from the OpenPrintTag
// catalogue the Materials extension holds: the nozzle and bed temperatures the
// maker advises, and the density. They replace the matching values of the
// generic preset a profile starts from, which knows nothing of the brand.

import (
	"strconv"
	"strings"
)

// Properties are what the catalogue says of one material. A zero is unknown.
type Properties struct {
	MinPrintTemp int     `json:"min_print_temp,omitempty"`
	MaxPrintTemp int     `json:"max_print_temp,omitempty"`
	MinBedTemp   int     `json:"min_bed_temp,omitempty"`
	MaxBedTemp   int     `json:"max_bed_temp,omitempty"`
	Density      float64 `json:"density,omitempty"`
}

// Ranges the catalogue's values must fall in to be believed. A value outside
// them is a typing error in a record, and writing it would burn a nozzle.
const (
	nozzleLow, nozzleHigh = 150, 350
	bedLow, bedHigh       = 20, 140
	densityLow            = 0.5
	densityHigh           = 5.0
)

// atoi reads a whole number of °C from a preset value.
func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))

	return n, err == nil
}

// midpoint is the middle of a range, rounded to a multiple of step, and kept
// inside the range.
func midpoint(lo, hi, step int) int {
	mid := (lo + hi) / 2
	mid = (mid + step/2) / step * step

	return min(max(mid, lo), hi)
}

// offset is how much a preset's first-layer value differs from its own, so the
// brand's temperature keeps the margin Prusa chose. It is kept within a few
// degrees: a preset that sets the two far apart is not a margin.
func offset(vals map[string]string, first, base string) int {
	a, okA := atoi(vals[first])
	b, okB := atoi(vals[base])

	if !okA || !okB {
		return 0
	}

	return min(max(a-b, -10), 20)
}

// apply writes the catalogue's temperatures and density over vals through set,
// and reports whether it changed anything.
func (p *Properties) apply(vals map[string]string, set func(key, value string)) bool {
	if p == nil {
		return false
	}

	changed := false

	if p.MinPrintTemp >= nozzleLow && p.MaxPrintTemp <= nozzleHigh && p.MinPrintTemp <= p.MaxPrintTemp {
		temp := midpoint(p.MinPrintTemp, p.MaxPrintTemp, 5)
		first := temp + offset(vals, "first_layer_temperature", "temperature")

		set("temperature", strconv.Itoa(temp))
		set("first_layer_temperature", strconv.Itoa(first))

		changed = true
	}

	if p.MinBedTemp >= bedLow && p.MaxBedTemp <= bedHigh && p.MinBedTemp <= p.MaxBedTemp {
		bed := midpoint(p.MinBedTemp, p.MaxBedTemp, 1)
		first := bed + offset(vals, "first_layer_bed_temperature", "bed_temperature")

		set("bed_temperature", strconv.Itoa(bed))
		set("first_layer_bed_temperature", strconv.Itoa(min(max(first, bedLow), bedHigh)))

		changed = true
	}

	if p.Density >= densityLow && p.Density <= densityHigh {
		set("filament_density", strconv.FormatFloat(p.Density, 'f', -1, 64))

		changed = true
	}

	return changed
}
