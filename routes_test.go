package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

type queueAccess struct{}

func (queueAccess) Check(_ context.Context, p accessapi.CheckParams) (accessapi.Decision, error) {
	return accessapi.Decision{Allowed: p.ObjectID == "p1" &&
		(p.Action == "observe" || (p.Subject == "user:operator" && p.Action == "operate"))}, nil
}

func TestQueueRoutesUseTheCallersPrinterGrants(t *testing.T) {
	store, err := OpenStore(t.TempDir(), time.Now, 20)
	if err != nil {
		t.Fatal(err)
	}
	visible, _ := store.Add(Job{DeviceID: "p1", Path: "usb/visible.gcode"})
	hidden, _ := store.Add(Job{DeviceID: "p2", Path: "usb/hidden.gcode"})
	if err := store.SetPaused("p2", true); err != nil {
		t.Fatal(err)
	}
	routes := (&module{store: store, access: queueAccess{}}).routes()

	request := func(method, path, principal, body string) *httptest.ResponseRecorder {
		t.Helper()
		r, err := (routesapi.Request{Method: method, Path: path, Principal: principal, Body: []byte(body)}).HTTP(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w
	}

	list := request("GET", "/jobs", "user:viewer", "")
	var view View
	if err := json.Unmarshal(list.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if list.Code != http.StatusOK || len(view.Jobs) != 1 || view.Jobs[0].ID != visible.ID || len(view.Paused) != 0 {
		t.Fatalf("unexpected queue for viewer: %d %+v", list.Code, view)
	}

	for _, test := range []struct {
		method, path, principal, body string
		want                          int
	}{
		{"GET", "/jobs?device=p2", "user:viewer", "", http.StatusNotFound},
		{"DELETE", "/jobs/" + hidden.ID, "user:operator", "", http.StatusNotFound},
		{"DELETE", "/jobs/" + visible.ID, "user:viewer", "", http.StatusForbidden},
		{"POST", "/jobs", "user:viewer", `{"device_id":"p1","path":"usb/test.gcode"}`, http.StatusForbidden},
		{"PUT", "/devices/p2/paused", "user:operator", `{"paused":false}`, http.StatusForbidden},
		{"DELETE", "/jobs/" + visible.ID, "user:operator", "", http.StatusNoContent},
	} {
		if got := request(test.method, test.path, test.principal, test.body).Code; got != test.want {
			t.Errorf("%s %s as %s: got %d, want %d", test.method, test.path, test.principal, got, test.want)
		}
	}
}

func TestClearFinishedOnlyTouchesAuthorizedPrinters(t *testing.T) {
	store, _, sc, c := setup(t, printerReady)
	allowed, _ := store.Add(Job{DeviceID: "p1", Path: "usb/allowed.gcode"})
	hidden, _ := store.Add(Job{DeviceID: "p2", Path: "usb/hidden.gcode"})
	if err := store.update(func(data *snapshot) error {
		for i := range data.Jobs {
			data.Jobs[i].Status = statusDone
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := sc.applyWindow(busapi.Window{Cursor: "last-window"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenStore(filepath.Dir(store.file), c.now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.data.Cursor != "last-window" {
		t.Fatal("the event cursor was not saved")
	}

	r, err := (routesapi.Request{
		Method: http.MethodPost, Path: "/jobs/clear-finished", Principal: "user:operator", Body: []byte("{}"),
	}).HTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&module{store: reloaded, access: queueAccess{}}).routes().ServeHTTP(w, r)
	_, visible := reloaded.job(allowed.ID)
	_, hiddenRemains := reloaded.job(hidden.ID)
	if w.Code != http.StatusNoContent || visible || !hiddenRemains {
		t.Fatalf("clear returned %d, authorized job remains: %t, hidden job remains: %t", w.Code, visible, hiddenRemains)
	}
}

func TestPlateSettingsRequireAnOperatorAndSurviveRestart(t *testing.T) {
	store, _, sc, clock := setup(t, "IDLE")
	dir := filepath.Dir(store.file)
	settings := Settings{PlatePrinter: "p1", PlateCameraURL: "http://127.0.0.1:8080/api/v1/cameras/p1/snapshot"}
	p, err := openPlateCheck(dir, settings, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	img := platePhoto(45, false)
	jpg := encodeTestJPEG(t, img)
	p.snapshot = func(context.Context) (plateFrame, error) {
		return plateFrame{jpeg: jpg, image: img, taken: clock.at}, nil
	}
	sc.plate = p
	routes := (&module{store: store, access: queueAccess{}, scheduler: sc}).routes()
	const enable = `{"action":"set_automatic","automatic":true}`
	const capture = `{"action":"capture_reference"}`
	for _, step := range []struct {
		principal, body string
		want            int
		automatic       bool
	}{
		{"user:viewer", capture, http.StatusForbidden, false},
		{"user:operator", enable, http.StatusConflict, false},
		{"user:operator", capture, http.StatusNoContent, false},
		{"user:operator", enable, http.StatusNoContent, true},
		{"user:viewer", `{"action":"set_automatic","automatic":false}`, http.StatusForbidden, true},
		{"user:operator", capture, http.StatusNoContent, false},
	} {
		req, err := (routesapi.Request{
			Method: http.MethodPost, Path: "/devices/p1/plate-check", Principal: step.principal, Body: []byte(step.body),
		}).HTTP(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRecorder()
		routes.ServeHTTP(r, req)
		if r.Code != step.want {
			t.Fatalf("%s %s returned %d, want %d: %s", step.principal, step.body, r.Code, step.want, r.Body)
		}
		reloaded, err := openPlateCheck(dir, settings, clock.now)
		if err != nil {
			t.Fatal(err)
		}
		if (reloaded.settings.EnabledBy != "") != step.automatic ||
			(reloaded.reference != nil) != (p.reference != nil) {
			t.Fatalf("saved plate settings are wrong after %s", step.body)
		}
	}
	settings.PlateCameraURL = "http://127.0.0.1:8080/api/v1/cameras/different/snapshot"
	reloaded, err := openPlateCheck(dir, settings, clock.now)
	if err != nil || reloaded.reference != nil || reloaded.settings.EnabledBy != "" {
		t.Fatalf("changing camera must invalidate the reference: %v", err)
	}
}
