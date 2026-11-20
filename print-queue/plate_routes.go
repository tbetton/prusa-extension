package main

import (
	"context"
	"net/http"

	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

func (m *module) mayConfigurePlate(ctx context.Context, principal, id string) bool {
	return m.allowed(ctx, principal, "operate", "device", id)
}

func (m *module) previewPlate(w http.ResponseWriter, r *http.Request) {
	if m.ready(w, r) == nil {
		return
	}
	id := r.PathValue("id")
	if !m.allowed(r.Context(), routesapi.PrincipalOf(r), "observe", "device", id) {
		refuse(w, r, NoDevice.New("printer unavailable"))
		return
	}
	sc := m.scheduler
	sc.mu.Lock()
	p := sc.plate
	configured := p != nil && p.settings.Device == id
	sc.mu.Unlock()
	if !configured {
		refuse(w, r, NoDevice.New("no plate camera for this printer"))
		return
	}
	frame, err := p.snapshot(r.Context())
	if err != nil {
		refuse(w, r, PlateCheckUnavailable.New(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(frame.jpeg)
}

func (m *module) plateViews(ctx context.Context, principal, device string) map[string]plateView {
	sc := m.scheduler
	views := make(map[string]plateView)
	sc.store.mu.Lock()
	for id := range sc.store.data.PlateReady {
		views[id] = plateView{Status: "readyBlocked"}
	}
	sc.store.mu.Unlock()
	var configured string
	if p := sc.plate; p != nil {
		sc.mu.Lock()
		configured = p.settings.Device
		view := plateView{Automatic: p.settings.EnabledBy != "", Status: p.status, LastMatchAt: p.lastMatch}
		view.ProfileCount = len(p.profiles)
		at := p.settings.ReferenceAt
		for _, profile := range p.settings.Profiles {
			if profile.At.After(at) {
				at = profile.At
			}
		}
		if !at.IsZero() {
			view.ReferenceAt = &at
		}
		if p.calibration != nil {
			view.Status = "calibrating"
			view.Calibration = &plateCalibrationView{
				Captured: len(p.calibration.frames), Target: plateCalibrationFrames, Issue: p.calibration.issue,
			}
		} else if p.calibrationFailure != "" {
			view.Calibration = &plateCalibrationView{Failed: true, Issue: p.calibrationFailure}
		}
		views[configured] = view
		sc.mu.Unlock()
	}

	for id, view := range views {
		if (device != "" && id != device) || !m.allowed(ctx, principal, "observe", "device", id) {
			delete(views, id)
			continue
		}
		view.CanConfigure = id == configured && m.mayConfigurePlate(ctx, principal, id)
		views[id] = view
	}
	return views
}

func (m *module) updatePlateCheck(w http.ResponseWriter, r *http.Request) {
	if m.ready(w, r) == nil {
		return
	}
	id := r.PathValue("id")
	if !m.requireOperate(w, r, id) {
		return
	}
	var body struct {
		Action    string `json:"action"`
		Automatic *bool  `json:"automatic"`
	}
	if !decode(w, r, &body) {
		return
	}
	valid := body.Action == "set_automatic" && body.Automatic != nil
	switch body.Action {
	case "capture_reference", "start_calibration", "cancel_calibration":
		valid = body.Automatic == nil
	}
	if !valid {
		refuse(w, r, BadRequest.New("expected a plate action, with a boolean only for set_automatic"))
		return
	}

	sc := m.scheduler
	if sc == nil {
		refuse(w, r, NoDevice.New("plate check is not configured for this printer"))
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	p := sc.plate
	if p == nil || p.settings.Device != id {
		refuse(w, r, NoDevice.New("plate check is not configured for this printer"))
		return
	}
	if body.Action == "cancel_calibration" {
		p.calibration, p.calibrationFailure = nil, ""
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if p.calibration != nil {
		refuse(w, r, PlateCheckUnavailable.New("wait for the calibration to finish or cancel it"))
		return
	}
	if body.Action == "start_calibration" {
		if p.permit.job != "" {
			refuse(w, r, PlateCheckUnavailable.New("wait for the current automatic start to finish"))
			return
		}
		if err := p.startCalibration(r.Context(), sc, routesapi.PrincipalOf(r)); err != nil {
			refuse(w, r, PlateCheckUnavailable.New(err.Error()))
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	next, reference := p.settings, p.reference
	if body.Action == "capture_reference" {
		var err error
		next, reference, err = p.capture(r.Context(), sc)
		if err != nil {
			refuse(w, r, PlateCheckUnavailable.New(err.Error()))
			return
		}
	} else if *body.Automatic {
		if !p.hasReference() {
			refuse(w, r, PlateCheckUnavailable.New("capture the empty plate before enabling automatic readiness"))
			return
		}
		next.EnabledBy = routesapi.PrincipalOf(r)
	} else {
		next.EnabledBy = ""
	}

	if err := p.save(next); err != nil {
		logf("save plate check: %v", err)
		refuse(w, r, StoreFailed.New("the plate settings could not be saved"))
		return
	}
	p.reference = reference
	if body.Action == "capture_reference" {
		p.lastMatch = nil
		p.calibrationFailure = ""
	}
	p.permit = platePermit{}
	switch {
	case !p.hasReference():
		p.reset("noReference")
	default:
		p.reset("waiting")
	}
	w.WriteHeader(http.StatusNoContent)
}
