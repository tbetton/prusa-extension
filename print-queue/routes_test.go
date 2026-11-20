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
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
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
	routes := (&module{store: store, access: queueAccess{}, slicerUploadFolder: "from-slicer/jobs"}).routes()

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

	destination := request("GET", "/devices/p1/slicer-upload", "user:operator", "")
	var upload map[string]string
	if err := json.Unmarshal(destination.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	if destination.Code != http.StatusOK || upload["folder"] != "from-slicer/jobs" {
		t.Fatalf("unexpected slicer destination: %d %+v", destination.Code, upload)
	}

	for _, test := range []struct {
		method, path, principal, body string
		want                          int
	}{
		{"GET", "/devices/p1/slicer-upload", "user:viewer", "", http.StatusForbidden},
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

type readyFleet struct{ *fakeFleet }

func (f readyFleet) Get(ctx context.Context, args deviceapi.GetParams) (deviceapi.Device, error) {
	device, err := f.fakeFleet.Get(ctx, args)
	device.Capabilities = []string{readyVerb}
	return device, err
}

func TestManualReadyWaitsForPrinterConfirmation(t *testing.T) {
	store, fleet, sc, _ := setup(t, "FINISHED")
	sc.fleet = readyFleet{fleet}
	var commands int
	sc.command = func(_ context.Context, p deviceapi.CommandParams) (deviceapi.Result, error) {
		if p.ID != "p1" || p.Verb != readyVerb {
			t.Fatalf("unexpected command: %+v", p)
		}
		commands++
		return deviceapi.Result{OK: true}, nil
	}
	_, _ = store.Add(Job{DeviceID: "p1", Path: "next.bgcode"})
	if err := store.update(func(data *snapshot) error {
		data.UnconfirmedReady = map[string]readyMarker{"p1": {}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	routes := (&module{store: store, access: queueAccess{}, scheduler: sc}).routes()
	for _, step := range []struct {
		principal, body, state string
		want                   int
	}{
		{"user:viewer", `{"plate_empty":true}`, "FINISHED", http.StatusForbidden},
		{"user:operator", `{"plate_empty":false}`, "FINISHED", http.StatusBadRequest},
		{"user:operator", `{"plate_empty":true}`, "PRINTING", http.StatusConflict},
		{"user:operator", `{"plate_empty":true}`, "FINISHED", http.StatusNoContent},
	} {
		fleet.state = step.state
		req, err := (routesapi.Request{
			Method: http.MethodPost, Path: "/devices/p1/ready", Principal: step.principal, Body: []byte(step.body),
		}).HTTP(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		if w.Code != step.want {
			t.Fatalf("READY as %s in %s returned %d, want %d: %s", step.principal, step.state, w.Code, step.want, w.Body)
		}
	}
	sc.Tick(t.Context())
	if commands != 1 || len(fleet.printed) != 0 {
		t.Fatalf("wanted one READY command and no prints before READY, got %d and %d", commands, len(fleet.printed))
	}
	fleet.state = printerReady
	sc.Tick(t.Context())
	if len(fleet.printed) != 1 {
		t.Fatal("the queue did not dispatch after the printer reported READY")
	}
}

func TestUpgradeRequiresConfirmationForAnAutomaticReady(t *testing.T) {
	store, fleet, sc, c := setup(t, printerReady)
	if _, err := store.Add(Job{DeviceID: "p1", Path: "next.bgcode"}); err != nil {
		t.Fatal(err)
	}
	if err := store.update(func(data *snapshot) error {
		data.UnconfirmedReady = map[string]readyMarker{"p1": {}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(filepath.Dir(store.file), c.now, 20)
	if err != nil {
		t.Fatal(err)
	}
	sc.store = upgraded
	sc.command = func(context.Context, deviceapi.CommandParams) (deviceapi.Result, error) {
		t.Fatal("a printer already reporting READY should not receive another READY command")
		return deviceapi.Result{}, nil
	}
	sc.Tick(t.Context())
	if len(fleet.printed) != 0 || !upgraded.List("p1").ReadyConfirmationRequired["p1"] {
		t.Fatal("the queue must wait for confirmation before using a READY from an earlier release")
	}

	req, err := (routesapi.Request{
		Method: http.MethodPost, Path: "/devices/p1/ready", Principal: "user:operator", Body: []byte(`{"plate_empty":true}`),
	}).HTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&module{store: upgraded, access: queueAccess{}, scheduler: sc}).routes().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("READY confirmation returned %d: %s", w.Code, w.Body)
	}
	sc.Tick(t.Context())
	if len(fleet.printed) != 1 {
		t.Fatal("the queue did not dispatch after manual confirmation")
	}
}
