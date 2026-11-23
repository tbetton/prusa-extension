package main

import (
	"reflect"
	"strings"
	"testing"
)

// fixture is a small vendor bundle with the shapes of the real one: an
// abstract base, a chain of parents, printer variants and a section that
// repeats a key.
const fixture = `# Print profiles for Prusa Research printers.

[vendor]
name = Prusa Research
config_version = 2.5.16

[printer_model:COREONE]
name = Prusa CORE One

[filament:*common*]
filament_cost = 0
temperature = 200
filament_notes = ""

[filament:*PLA*]
inherits = *common*
filament_type = PLA
temperature = 215
bed_temperature = 60

[filament:*PLAIND*]
temperature = 225
fan_always_on = 1

[filament:Prusament PLA]
inherits = *PLA*
filament_vendor = Prusa Polymers
alias = Prusament PLA
filament_density = 1.24
compatible_printers_condition = nozzle_diameter[0]!=0.8

[filament:Prusament PLA @COREONEINDX HF0.4]
inherits = Prusament PLA; *PLAIND*
alias = Prusament PLA
renamed_from = old
temperature = 230

[filament:Prusament PLA @COREONEINDX 0.25]
inherits = Prusament PLA
temperature = 220

[filament:Generic PLA]
inherits = *PLA*
filament_vendor = (Unknown)

[filament:Generic PLA @COREONEINDX HF0.4]
inherits = Generic PLA
temperature = 228

[filament:Generic PETG]
inherits = *common*
filament_type = PETG

[filament:Polymaker PolyLite PLA]
inherits = *PLA*

[filament:Filatech Wood-PLA]
inherits = *PLA*
`

func TestParseKeepsFilamentSectionsOnly(t *testing.T) {
	c := ParseCatalogue(fixture)

	if c.Version != "2.5.16" {
		t.Fatalf("version = %q", c.Version)
	}

	if c.Len() != 11 {
		t.Fatalf("sections = %d, want 11", c.Len())
	}

	want := []string{
		"Prusament PLA", "Prusament PLA @COREONEINDX HF0.4", "Prusament PLA @COREONEINDX 0.25",
		"Generic PLA", "Generic PLA @COREONEINDX HF0.4", "Generic PETG", "Polymaker PolyLite PLA", "Filatech Wood-PLA",
	}
	if got := c.Presets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("presets = %v, want %v", got, want)
	}
}

func TestFlattenFoldsParentsInOrder(t *testing.T) {
	c := ParseCatalogue(fixture)

	keys, vals, err := c.Flatten("Prusament PLA @COREONEINDX HF0.4")
	if err != nil {
		t.Fatal(err)
	}

	// *PLA* sets 215, *PLAIND* 225, the section itself 230: the last wins.
	if vals["temperature"] != "230" {
		t.Fatalf("temperature = %q", vals["temperature"])
	}

	// The grandparent's keys survive, and `inherits` does not.
	if vals["filament_type"] != "PLA" || vals["filament_density"] != "1.24" || vals["fan_always_on"] != "1" {
		t.Fatalf("a parent's key was lost: %v", vals)
	}

	if _, ok := vals["inherits"]; ok {
		t.Fatal("inherits was kept")
	}

	seen := map[string]bool{}

	for _, k := range keys {
		if seen[k] {
			t.Fatalf("key %q listed twice", k)
		}

		seen[k] = true
	}
}

func TestFlattenRefusesAMissingParentAndACycle(t *testing.T) {
	c := ParseCatalogue("[filament:A]\ninherits = Ghost\n\n[filament:B]\ninherits = C\n\n[filament:C]\ninherits = B\n")

	if _, _, err := c.Flatten("A"); err == nil || !strings.Contains(err.Error(), "Ghost") {
		t.Fatalf("a missing parent gave %v", err)
	}

	if _, _, err := c.Flatten("B"); err == nil {
		t.Fatal("a cycle was folded")
	}
}

func TestVariantsListsTheSuffixesOfEachBase(t *testing.T) {
	got := ParseCatalogue(fixture).Variants()

	if want := []string{"COREONEINDX 0.25", "COREONEINDX HF0.4"}; !reflect.DeepEqual(got["Prusament PLA"], want) {
		t.Fatalf("variants = %v, want %v", got["Prusament PLA"], want)
	}

	if len(got["Generic PETG"]) != 0 {
		t.Fatalf("a base with no variant has %v", got["Generic PETG"])
	}

	if _, ok := got["Prusament PLA @COREONEINDX 0.25"]; ok {
		t.Fatal("a variant was listed as a base")
	}
}

func TestBestMatchesPrefersTheBrandAndEndsWithGeneric(t *testing.T) {
	c := ParseCatalogue(fixture)

	got := c.BestMatches("Prusament", "PLA Galaxy Black", "PLA", 5, nil)
	if len(got) != 2 || got[0].Preset != "Prusament PLA" || got[1].Preset != "Generic PLA" {
		t.Fatalf("matches = %+v", got)
	}

	// A brand the bundle knows nothing about still gets the generic preset.
	got = c.BestMatches("Sunlu", "High Speed PLA Yellow", "pla", 5, nil)
	if len(got) != 1 || got[0].Preset != "Generic PLA" {
		t.Fatalf("matches = %+v", got)
	}

	// "Polymaker PolyLite PLA" is a real match for a Polymaker spool.
	got = c.BestMatches("Polymaker", "PolyLite™ PLA Black", "PLA", 5, nil)
	if got[0].Preset != "Polymaker PolyLite PLA" {
		t.Fatalf("matches = %+v", got)
	}

	// No type, no generic: nothing to fall back to.
	if got = c.BestMatches("Sunlu", "Mystery", "", 5, nil); len(got) != 0 {
		t.Fatalf("matches = %+v", got)
	}
}

func TestBestMatchesOnlyTakesPresetsOfTheSpoolsBrand(t *testing.T) {
	c := ParseCatalogue(fixture)

	// "Filatech Wood-PLA" shares "wood" and "pla" with this spool, but it is
	// another brand's: only the generic preset is offered.
	got := c.BestMatches("Geeetech", "Wood PLA Poplar", "PLA", 20, nil)
	if len(got) != 1 || got[0].Preset != "Generic PLA" {
		t.Fatalf("matches = %+v", got)
	}

	// With no brand named, the words alone decide.
	got = c.BestMatches("", "Filatech Wood PLA", "PLA", 20, nil)
	if len(got) != 2 || got[0].Preset != "Filatech Wood-PLA" {
		t.Fatalf("matches = %+v", got)
	}
}
