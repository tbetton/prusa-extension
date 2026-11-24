package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// printerFixture has the shapes of the real bundle that decide compatibility:
// printers with notes, a model and nozzles, a generic preset that excludes the
// printers whose notes carry "PG", its variant for the INDX, a preset that
// excludes them too, an explicit list, and a condition that cannot be read.
const printerFixture = `[vendor]
config_version = 2.5.16

[printer:*common*]
printer_technology = FFF
single_extruder_multi_material = 0

[printer:Indx 8T HF0.4 nozzle]
inherits = *common*
printer_model = COREONE_INDX8T
printer_variant = HF0.4
nozzle_diameter = 0.4,0.4,0.4,0.4,0.4,0.4,0.4,0.4
nozzle_high_flow = 1,1,1,1,1,1,1,1
printer_notes = PRINTER_VENDOR_PRUSA3D\nPRINTER_MODEL_COREONE_INDX\nPG

[printer:Core One 0.4 nozzle]
inherits = *common*
printer_model = COREONE
nozzle_diameter = 0.4
nozzle_high_flow = 0
printer_notes = PRINTER_VENDOR_PRUSA3D\nPRINTER_MODEL_COREONE

[printer:Mk4 0.4 nozzle]
inherits = *common*
printer_model = MK4
nozzle_diameter = 0.4
nozzle_high_flow = 0
printer_notes = PRINTER_VENDOR_PRUSA3D\nPRINTER_MODEL_MK4

[filament:*PLA*]
filament_type = PLA
temperature = 215
bed_temperature = 60
filament_max_volumetric_speed = 15
start_filament_gcode = "; generic"

[filament:Generic PLA]
inherits = *PLA*
compatible_printers_condition = printer_notes!~/.*PG.*/ and nozzle_diameter[0]!=0.8

[filament:Generic PLA @INDX HF0.4]
inherits = Generic PLA
compatible_printers_condition = printer_model=~/(COREONE_INDX8T|COREONE_INDX4T)/ and nozzle_diameter[0]==0.4 and nozzle_high_flow[0]
temperature = 228
filament_max_volumetric_speed = 24
start_filament_gcode = "M142 S40 ; indx"

[filament:Elegoo PLA]
inherits = *PLA*
compatible_printers_condition = printer_notes=~/.*PRINTER_VENDOR_PRUSA3D.*/ and ! (printer_notes=~/.*PRINTER_MODEL_MK(2.5|3).*/ and single_extruder_multi_material) and printer_notes!~/.*PG.*/

[filament:Listed]
inherits = *PLA*
compatible_printers = "Mk4 0.4 nozzle"
compatible_printers_condition = printer_model=="nothing"

[filament:Broken]
compatible_printers_condition = no_such_option == 3
`

// polymakerFixture is one preset as Polymaker publishes it, for the CORE One.
const polymakerFixture = `[filament:Panchroma PLA Matte @Prusa Core One]
filament_type = PLA
temperature = 205
bed_temperature = 55
filament_max_volumetric_speed = 15
filament_retract_length = 0.8
start_filament_gcode = "M142 S36 ; core one"
compatible_printers_condition = printer_model=~/(COREONE|COREONEL)/ and nozzle_diameter[0]==0.4
`

const (
	indx    = "Indx 8T HF0.4 nozzle"
	coreOne = "Core One 0.4 nozzle"
	mk4     = "Mk4 0.4 nozzle"
)

func withPolymaker(t *testing.T) *Catalogue {
	t.Helper()

	c := ParseCatalogue(printerFixture)
	if added := c.AddSource("polymaker", "polymaker", regexp.MustCompile(`^COREONE`), polymakerFixture); added != 1 {
		t.Fatalf("added %d presets, want 1", added)
	}

	return c
}

func TestConditionLanguage(t *testing.T) {
	env := map[string]string{"a": "x", "n": "0.4,0.6", "f": "1,0", "z": "0", "notes": "A\nPG\nB"}

	for cond, want := range map[string]bool{
		`a == "x"`:                  true,
		`a != "x"`:                  false,
		`"x" == a`:                  true,
		`n[0]==0.4`:                 true,
		`n[1] == 0.6`:               true,
		`n[1]==0.4`:                 false,
		`n[0] < n[1]`:               true,
		`f[0]`:                      true,
		`f[1]`:                      false,
		`z`:                         false,
		`!z`:                        true,
		`a=~/x/`:                    true,
		`a=~/y/`:                    false,
		`a!~/y/`:                    true,
		`notes=~/.*PG.*/`:           true,
		`notes=~/PG/`:               false,
		`1 == 1.0`:                  true,
		`a == 3`:                    false,
		`a != 3`:                    true,
		`true and (false or true)`:  true,
		`true && !true || false`:    false,
		`single_extruder_multi_material`: true,
		`nozzle_high_flow[0]`:       false,
		`a=~/(x|y)/ and n[0]!=0.8`:  true,
		`a=~/a\/b/`:                 false,
	} {
		got, err := evalCondition(cond, env)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", cond, got, err, want)
		}
	}
}

func TestConditionErrors(t *testing.T) {
	env := map[string]string{"a": "x", "n": "0.4"}

	for _, cond := range []string{
		`missing == 1`, `a ==`, `a == "x" )`, `(a == "x"`, `n[5]`, `n[`, `/abc`, `"abc`, `a =~ "x"`, `a @ b`, `a =~ /(/`,
	} {
		if _, err := evalCondition(cond, env); err == nil {
			t.Errorf("%q was accepted", cond)
		} else if !errors.Is(err, errCondition) {
			t.Errorf("%q: %v is not a condition error", cond, err)
		}
	}
}

func TestCompatibleDecidesLikePrusaSlicer(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	for _, tc := range []struct {
		filament, printer string
		want              bool
	}{
		{"Generic PLA", indx, false}, // the INDX notes carry PG
		{"Generic PLA @INDX HF0.4", indx, true},
		{"Generic PLA", coreOne, true},
		{"Generic PLA @INDX HF0.4", coreOne, false},
		{"Generic PLA", mk4, true},
		{"Elegoo PLA", indx, false},
		{"Elegoo PLA", coreOne, true},
		{"Elegoo PLA", mk4, true},
		{"Listed", mk4, true}, // the explicit list wins over the condition
		{"Listed", coreOne, false},
	} {
		got, err := c.Compatible(tc.filament, tc.printer)
		if err != nil || got != tc.want {
			t.Errorf("%s on %s = %v, %v; want %v", tc.filament, tc.printer, got, err, tc.want)
		}
	}

	if _, err := c.Compatible("Broken", indx); err == nil {
		t.Error("a condition with an unknown option was decided")
	}

	if _, err := c.Compatible("Generic PLA", "Nope"); !errors.Is(err, errNoPrinter) {
		t.Errorf("an unknown printer gave %v", err)
	}
}

func TestSectionForPicksTheVariantThatFits(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	for _, tc := range []struct{ base, printer, want string }{
		{"Generic PLA", indx, "Generic PLA @INDX HF0.4"},
		{"Generic PLA", coreOne, "Generic PLA"},
		{"Generic PLA", mk4, "Generic PLA"},
		{"Elegoo PLA", indx, ""},
		{"Elegoo PLA", coreOne, "Elegoo PLA"},
		{"Broken", indx, ""}, // left out rather than offered wrong
	} {
		got, err := c.SectionFor(tc.base, tc.printer)
		if err != nil || got != tc.want {
			t.Errorf("%s on %s = %q, %v; want %q", tc.base, tc.printer, got, err, tc.want)
		}
	}

	if !c.Fits("Generic PLA", indx) || c.Fits("Elegoo PLA", indx) {
		t.Error("Fits disagrees with SectionFor")
	}
}

func TestPrintersListsThePresetsToChooseFrom(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	all := c.Printers(nil)

	names := make([]string, len(all))
	for i, p := range all {
		names[i] = p.Name
	}

	if want := []string{coreOne, indx, mk4}; !reflect.DeepEqual(names, want) {
		t.Fatalf("printers = %v, want %v", names, want)
	}

	got := c.Printers(normalise("indx"))
	want := []PrinterInfo{{Name: indx, Model: "COREONE_INDX8T", Variant: "HF0.4", Extruder: 8, Nozzle: 0.4, HighFlow: true}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("indx = %+v, want %+v", got, want)
	}

	if one := c.Printers(normalise("core one"))[0]; one.Extruder != 1 || one.HighFlow {
		t.Fatalf("core one = %+v", one)
	}
}

func TestBundleForcesThePrinterAndTheSectionThatFits(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	text, err := c.Bundle(indx, []Request{
		{Name: "Mine", Base: "Generic PLA", Slot: 1},
		{Name: "Other", Base: "Generic PLA", Slot: 3},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"[filament:Mine @INDX HF0.4]\n", "[filament:Other @INDX HF0.4]\n", "temperature = 228\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("bundle misses %q:\n%s", want, text)
		}
	}

	tail := "\n[presets]\nprinter = " + indx + "\nfilament = Mine @INDX HF0.4\nfilament_2 = Other @INDX HF0.4\n"
	if !strings.HasSuffix(text, tail) {
		t.Fatalf("the bundle does not end with %q:\n%s", tail, text)
	}

	// A printer alone is still forced.
	text, err = c.Bundle(coreOne, []Request{{Name: "Mine", Base: "Generic PLA"}})
	if err != nil || !strings.HasSuffix(text, "\n[presets]\nprinter = "+coreOne+"\n") {
		t.Fatalf("printer only: %v\n%s", err, text)
	}
}

func TestBundleWritesAPresetThatDoesNotFitAndRefusesAPrinterItDoesNotKnow(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	// A preset that fits no section of the printer is still written: it is
	// laid over a generic preset, or freed from its compatibility.
	text, err := c.Bundle(indx, []Request{{Name: "Mine", Base: "Elegoo PLA"}})
	if err != nil || !strings.Contains(text, "[filament:Mine") {
		t.Errorf("a preset that does not fit gave %v\n%s", err, text)
	}

	if _, err := c.Bundle(indx, []Request{{Name: "Mine", Base: "Nope"}}); !errors.Is(err, errNoPreset) {
		t.Errorf("an unknown preset gave %v", err)
	}

	if _, err := c.Bundle("Nope", []Request{{Name: "Mine", Base: "Generic PLA"}}); !errors.Is(err, errNoPrinter) {
		t.Errorf("an unknown printer gave %v", err)
	}
}

func TestAPresetOfAnotherSourceIsAdaptedToThePrinter(t *testing.T) {
	c := withPolymaker(t)

	// On the INDX the preset does not fit as written, but the printer is of
	// the CORE One family: it is laid over the generic preset that fits.
	res, err := c.Resolve("Panchroma PLA Matte", indx)
	if err != nil || res.Section != "Panchroma PLA Matte @Prusa Core One" || res.Reference != "Generic PLA @INDX HF0.4" {
		t.Fatalf("resolve on the INDX = %+v, %v", res, err)
	}

	// On a CORE One it fits as written.
	res, err = c.Resolve("Panchroma PLA Matte", coreOne)
	if err != nil || res.Section != "Panchroma PLA Matte @Prusa Core One" || res.Reference != "" {
		t.Fatalf("resolve on the CORE One = %+v, %v", res, err)
	}

	// An MK4 is not of the family: the preset is not offered.
	if res, err = c.Resolve("Panchroma PLA Matte", mk4); err != nil || res.Section != "" {
		t.Fatalf("resolve on the MK4 = %+v, %v", res, err)
	}

	text, err := c.Bundle(indx, []Request{{Name: "Polymaker PolyLite PLA Black", Base: "Panchroma PLA Matte", Slot: 1}})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"[filament:Polymaker PolyLite PLA Black @INDX HF0.4]\n",
		"temperature = 205\n",                    // the maker's
		"bed_temperature = 55\n",                 // the maker's
		"filament_retract_length = 0.8\n",        // the maker's
		"filament_max_volumetric_speed = 15\n",   // the maker's is lower than the printer's 24
		`start_filament_gcode = "M142 S40 ; indx"`, // the printer's
		"adapted to Generic PLA @INDX HF0.4 for this printer",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("bundle misses %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, "core one") {
		t.Errorf("the CORE One's own G-code reached the profile:\n%s", text)
	}

	// On a CORE One the preset is written as it is, for that printer.
	text, err = c.Bundle(coreOne, []Request{{Name: "Mine", Base: "Panchroma PLA Matte"}})
	if err != nil || !strings.Contains(text, "[filament:Mine @Prusa Core One]\n") || !strings.Contains(text, `start_filament_gcode = "M142 S36 ; core one"`) {
		t.Fatalf("as written: %v\n%s", err, text)
	}
}

func TestAFlowAboveThePrintersIsNotKept(t *testing.T) {
	c := ParseCatalogue(printerFixture)
	c.AddSource("polymaker", "polymaker", regexp.MustCompile(`^COREONE`),
		strings.Replace(polymakerFixture, "filament_max_volumetric_speed = 15", "filament_max_volumetric_speed = 40", 1))

	text, err := c.Bundle(indx, []Request{{Name: "Mine", Base: "Panchroma PLA Matte"}})
	if err != nil || !strings.Contains(text, "filament_max_volumetric_speed = 24\n") {
		t.Fatalf("the generic preset's limit was not kept: %v\n%s", err, text)
	}
}

func TestSourcesAreChosenAndBrandedPresetsMatch(t *testing.T) {
	c := withPolymaker(t)

	if got := c.Counts(); got["prusa"] != 5 || got["polymaker"] != 1 {
		t.Fatalf("counts = %v", got)
	}

	got := c.BestMatches("Polymaker", "Panchroma™ Matte PLA Lava Red", "PLA", 20, nil)
	if len(got) == 0 || got[0].Preset != "Panchroma PLA Matte" {
		t.Fatalf("matches = %+v", got)
	}

	if c.BaseSource("Panchroma PLA Matte") != "polymaker" || c.BaseSource("Generic PLA") != "prusa" {
		t.Fatal("a base preset names the wrong source")
	}

	// Only Polymaker's presets are offered, but the printers and the generic
	// presets of Prusa stay there to adapt them.
	c.SetSources([]string{"polymaker"})

	if got := c.Presets(); !reflect.DeepEqual(got, []string{"Panchroma PLA Matte @Prusa Core One"}) {
		t.Fatalf("presets = %v", got)
	}

	if got := c.BestMatches("Polymaker", "Panchroma Matte PLA", "PLA", 20, nil); len(got) != 1 {
		t.Fatalf("a hidden generic preset was offered: %+v", got)
	}

	if res, err := c.Resolve("Panchroma PLA Matte", indx); err != nil || res.Reference != "Generic PLA @INDX HF0.4" {
		t.Fatalf("a hidden source's generic preset was not used to adapt: %+v, %v", res, err)
	}
}

func TestAddSourceKeepsWhatIsAlreadyThere(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	if added := c.AddSource("custom", "", nil, "[filament:Generic PLA]\ntemperature = 1\n\n[filament:New PLA]\ntemperature = 2\n"); added != 1 {
		t.Fatalf("added %d, want 1", added)
	}

	_, vals, err := c.Flatten("Generic PLA")
	if err != nil || vals["temperature"] != "215" {
		t.Fatalf("a source replaced a preset: %v %v", vals["temperature"], err)
	}

	if c.SourceOf("New PLA") != "custom" || c.SourceOf("Generic PLA") != "prusa" {
		t.Fatal("sources are mixed up")
	}
}

func TestParseSettingsOfSources(t *testing.T) {
	for in, want := range map[string][]string{
		"":                                {"prusa", "polymaker"},
		"bogus":                           {"prusa", "polymaker"},
		"Polymaker, PRUSA,custom,bogus,polymaker": {"polymaker", "prusa", "custom"},
		"prusa":                           {"prusa"},
	} {
		if got := parseSources(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseSources(%q) = %v, want %v", in, got, want)
		}
	}

	got := customURLs("https://a/x.ini, http://b/y.ini\nhttps://c/z.ini")
	if want := []string{"https://a/x.ini", "https://c/z.ini"}; !reflect.DeepEqual(got, want) {
		t.Errorf("customURLs = %v, want %v", got, want)
	}

	many := strings.Repeat("https://a/x.ini ", maxCustom+4)
	if got := customURLs(many); len(got) != maxCustom {
		t.Errorf("customURLs kept %d addresses, want %d", len(got), maxCustom)
	}
}

func TestPolymakerArchiveBecomesABundle(t *testing.T) {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for name, body := range map[string]string{
		"Polymaker-Preset-main/preset/Panchroma PLA Matte/Prusa/Core One/PrusaSlicer/Panchroma PLA Matte @Prusa Core One.ini": "# generated\nbed_temperature = 60\ncompatible_printers = \n\nnot a setting\ntemperature = 205\n",
		"Polymaker-Preset-main/preset/Panchroma PLA Matte/Prusa/Core One/OrcaSlicer/Other.json":                                "{}",
		"Polymaker-Preset-main/preset/Bad[name/Prusa/Core One/PrusaSlicer/Bad[name.ini":                                        "temperature = 1\n",
		"Polymaker-Preset-main/README.md":                                                                                      "text",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := polymakerBundle(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	want := "[filament:Panchroma PLA Matte @Prusa Core One]\nbed_temperature = 60\ncompatible_printers =\ntemperature = 205\n\n"
	if got != want {
		t.Fatalf("bundle = %q, want %q", got, want)
	}

	if _, err = polymakerBundle([]byte("not a zip")); err == nil {
		t.Error("garbage was read")
	}

	if _, err = polymakerBundle(archive(t, map[string]string{"x/README.md": "text"})); err == nil {
		t.Error("an archive with no preset was read")
	}

	// What it wrote reads back as a catalogue.
	if c := ParseCatalogue(got); !c.Has("Panchroma PLA Matte @Prusa Core One") {
		t.Error("the bundle does not read back")
	}
}

func TestRoutesFilterByPrinter(t *testing.T) {
	m := &module{configured: true, catalogue: withPolymaker(t)}

	rec := do(t, m, http.MethodGet, "/printers?q=indx", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), indx) || strings.Contains(rec.Body.String(), "Mk4") {
		t.Fatalf("printers: %d %s", rec.Code, rec.Body)
	}

	// Elegoo's preset does not fit the INDX: only the generic one is offered.
	rec = do(t, m, http.MethodGet, "/match?brand=Elegoo&name=PLA+Cyan&type=PLA&printer=Indx+8T+HF0.4+nozzle", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"Elegoo PLA"`) || !strings.Contains(rec.Body.String(), `"section":"Generic PLA @INDX HF0.4"`) {
		t.Fatalf("match with a printer: %d %s", rec.Code, rec.Body)
	}

	// Without a printer nothing is filtered.
	rec = do(t, m, http.MethodGet, "/match?brand=Elegoo&name=PLA+Cyan&type=PLA", "")
	if !strings.Contains(rec.Body.String(), `"Elegoo PLA"`) {
		t.Fatalf("match without a printer: %s", rec.Body)
	}

	// Polymaker's preset is offered on the INDX, marked as adapted.
	rec = do(t, m, http.MethodGet, "/match?brand=Polymaker&name=Panchroma+Matte+PLA&type=PLA&printer=Indx+8T+HF0.4+nozzle", "")
	if !strings.Contains(rec.Body.String(), `"preset":"Panchroma PLA Matte"`) || !strings.Contains(rec.Body.String(), `"adapted":true`) || !strings.Contains(rec.Body.String(), `"source":"polymaker"`) {
		t.Fatalf("match of a Polymaker preset: %s", rec.Body)
	}

	if rec = do(t, m, http.MethodGet, "/match?printer=Nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown printer gave %d", rec.Code)
	}

	rec = do(t, m, http.MethodGet, "/presets?q=panchroma&printer=Mk4+0.4+nozzle", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Panchroma") {
		t.Fatalf("search on an MK4: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, m, http.MethodPost, "/bundle", `{"printer":"Indx 8T HF0.4 nozzle","filaments":[{"name":"Mine","base":"Generic PLA","slot":1}]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "printer = "+indx) {
		t.Fatalf("bundle: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, m, http.MethodPost, "/bundle", `{"printer":"Nope","filaments":[{"name":"Mine","base":"Generic PLA"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a bundle for an unknown printer gave %d", rec.Code)
	}
}

func TestEditedSettingsWinAndAreChecked(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	text, err := c.Bundle(indx, []Request{{
		Name: "Mine", Base: "Generic PLA",
		Overrides: map[string]string{"temperature": "215", "filament_type": "PLA"},
	}})
	if err != nil || !strings.Contains(text, "\ntemperature = 215\n") {
		t.Fatalf("an edited temperature was lost: %v\n%s", err, text)
	}

	for _, bad := range []map[string]string{
		{"temperature": "9999"},
		{"temperature": "hot"},
		{"filament_type": "PLA\n[x]"},
		{"start_filament_gcode": "M104 S0"},
	} {
		if _, err := c.Bundle(indx, []Request{{Name: "Mine", Base: "Generic PLA", Overrides: bad}}); !errors.Is(err, errOverride) {
			t.Errorf("%v gave %v", bad, err)
		}
	}
}

func TestSettingsListTheEditableKeys(t *testing.T) {
	c := ParseCatalogue(printerFixture)

	settings, _, err := c.Settings(indx, Request{Base: "Generic PLA"})
	if err != nil || len(settings) != len(editable) {
		t.Fatalf("settings: %v %v", settings, err)
	}
}
