package main

import (
	"strings"
	"testing"
)

func TestBundleWritesAFlatSection(t *testing.T) {
	c := ParseCatalogue(fixture)

	text, err := c.Bundle([]Request{{
		Name:      "Polymaker PolyLite™ PLA Black",
		Vendor:    "Polymaker",
		Base:      "Generic PLA",
		Variant:   "COREONEINDX HF0.4",
		ColorRGBA: "#1a1b1cff",
	}})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"[filament:Polymaker PolyLite PLA Black @COREONEINDX HF0.4]\n",
		"temperature = 228\n",
		"filament_type = PLA\n",
		"filament_vendor = Polymaker\n",
		"filament_colour = #1A1B1C\n",
		`filament_notes = "Written by filament-profile from Generic PLA @COREONEINDX HF0.4 (Prusa Research bundle 2.5.16)"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("bundle misses %q:\n%s", want, text)
		}
	}

	for _, bad := range []string{"inherits", "alias =", "renamed_from"} {
		if strings.Contains(text, bad) {
			t.Errorf("bundle keeps %q:\n%s", bad, text)
		}
	}

	// A bundle read back is a catalogue whose section stands alone.
	back := ParseCatalogue(text)
	if !back.Has("Polymaker PolyLite PLA Black @COREONEINDX HF0.4") {
		t.Fatalf("the bundle does not read back:\n%s", text)
	}
}

func TestBundleKeepsTheBaseColourWithoutOne(t *testing.T) {
	c := ParseCatalogue(fixture)

	text, err := c.Bundle([]Request{{Name: "Mine", Base: "Prusament PLA", ColorRGBA: "red"}})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(text, "filament_colour") {
		t.Fatalf("an invalid colour was written:\n%s", text)
	}

	if !strings.Contains(text, "filament_vendor = Prusa Polymers") {
		t.Fatalf("the base vendor was lost:\n%s", text)
	}
}

func TestBundleRefusesWhatItCannotWrite(t *testing.T) {
	c := ParseCatalogue(fixture)

	cases := map[string][]Request{
		"no request":       nil,
		"no name":          {{Name: " ", Base: "Generic PLA"}},
		"no such preset":   {{Name: "X", Base: "Nope"}},
		"no such variant":  {{Name: "X", Base: "Generic PLA", Variant: "MK4"}},
		"duplicated names": {{Name: "X", Base: "Generic PLA"}, {Name: "x/", Base: "Generic PETG"}, {Name: "X", Base: "Generic PETG"}},
	}

	for label, requests := range cases {
		if _, err := c.Bundle(requests); err == nil {
			t.Errorf("%s: no error", label)
		}
	}
}

func TestPresetNameIsAFileName(t *testing.T) {
	if got := presetName("A/B:C*D?E\"F<G>H|I [J]\n™®"); got != "A-B-CDEFGH-I (J)" {
		t.Fatalf("presetName = %q", got)
	}
}

func TestBundleSelectsTheProfilesInTheirSlots(t *testing.T) {
	c := ParseCatalogue(fixture)

	text, err := c.Bundle([]Request{
		{Name: "Third", Base: "Generic PLA", Slot: 3},
		{Name: "First", Base: "Generic PLA", Variant: "COREONEINDX HF0.4", Slot: 1},
		{Name: "Loose", Base: "Generic PETG"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "\n[presets]\nfilament = First @COREONEINDX HF0.4\nfilament_2 = Third\n"
	if !strings.HasSuffix(text, want) {
		t.Fatalf("the bundle does not end with %q:\n%s", want, text)
	}

	if strings.Contains(text, "Loose\n") && strings.Contains(strings.SplitN(text, "[presets]", 2)[1], "Loose") {
		t.Fatalf("a profile with no slot was selected:\n%s", text)
	}
}

func TestBundleWritesNoPresetsWithoutSlots(t *testing.T) {
	text, err := ParseCatalogue(fixture).Bundle([]Request{{Name: "Mine", Base: "Generic PLA"}})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(text, "[presets]") {
		t.Fatalf("a [presets] section was written:\n%s", text)
	}
}

func TestBundleRefusesABadSlot(t *testing.T) {
	c := ParseCatalogue(fixture)

	cases := map[string][]Request{
		"negative":  {{Name: "A", Base: "Generic PLA", Slot: -1}},
		"too large": {{Name: "A", Base: "Generic PLA", Slot: MaxRequests + 1}},
		"taken":     {{Name: "A", Base: "Generic PLA", Slot: 2}, {Name: "B", Base: "Generic PETG", Slot: 2}},
	}

	for label, requests := range cases {
		if _, err := c.Bundle(requests); err == nil {
			t.Errorf("%s: no error", label)
		}
	}
}
