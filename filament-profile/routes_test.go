package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func readyModule() *module {
	return &module{configured: true, catalogue: ParseCatalogue(fixture)}
}

func do(t *testing.T, m *module, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	m.routes().ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))

	return rec
}

func TestMatchAnswersCandidatesWithVariants(t *testing.T) {
	rec := do(t, readyModule(), http.MethodGet, "/match?brand=Prusament&name=PLA+Galaxy&type=PLA", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var got struct {
		Matches []candidate `json:"matches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Matches) != 2 || got.Matches[0].Preset != "Prusament PLA" || len(got.Matches[0].Variants) != 2 {
		t.Fatalf("matches = %+v", got.Matches)
	}
}

func TestSearchFindsPresetsByWords(t *testing.T) {
	rec := do(t, readyModule(), http.MethodGet, "/presets?q=generic+pet", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Generic PETG") || strings.Contains(rec.Body.String(), "Prusament") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestBundleRouteWritesTheFileAndMapsErrors(t *testing.T) {
	m := readyModule()

	rec := do(t, m, http.MethodPost, "/bundle", `{"filaments":[{"name":"Mine","base":"Generic PLA"}]}`)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(rec.Body.String(), "[filament:Mine]") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	if rec = do(t, m, http.MethodPost, "/bundle", `{"filaments":[{"name":"Mine","base":"Nope"}]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown preset gave %d", rec.Code)
	}

	if rec = do(t, m, http.MethodPost, "/bundle", `{"filaments":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an empty bundle gave %d", rec.Code)
	}

	if rec = do(t, m, http.MethodPost, "/bundle", `nope`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad body gave %d", rec.Code)
	}
}

func TestRoutesWaitForTheCatalogue(t *testing.T) {
	if rec := do(t, &module{}, http.MethodGet, "/match", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unconfigured module gave %d", rec.Code)
	}

	if rec := do(t, &module{configured: true}, http.MethodGet, "/match", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a module with no catalogue gave %d", rec.Code)
	}

	if rec := do(t, readyModule(), http.MethodGet, "/nothing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown path gave %d", rec.Code)
	}
}

func TestCatalogueStatus(t *testing.T) {
	rec := do(t, readyModule(), http.MethodGet, "/catalogue", "")

	var got status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if !got.Ready || got.Version != "2.5.16" || got.Presets != 7 {
		t.Fatalf("status = %+v", got)
	}
}
