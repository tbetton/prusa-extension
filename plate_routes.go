package main

import (
	"context"
	"net/http"

	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

func (m *module) mayConfigurePlate(ctx context.Context, principal, id string) bool {
	return m.allowed(ctx, principal, "operate", "device", id)
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
		if !p.settings.ReferenceAt.IsZero() {
			at := p.settings.ReferenceAt
			view.ReferenceAt = &at
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
	if (body.Action != "capture_reference" || body.Automatic != nil) &&
		(body.Action != "set_automatic" || body.Automatic == nil) {
		refuse(w, r, BadRequest.New("expected capture_reference or set_automatic with a boolean"))
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

	next, reference := p.settings, p.reference
	if body.Action == "capture_reference" {
		var err error
		next, reference, err = p.capture(r.Context(), sc)
		if err != nil {
			refuse(w, r, PlateCheckUnavailable.New(err.Error()))
			return
		}
	} else if *body.Automatic {
		if reference == nil {
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
	}
	p.permit = platePermit{}
	switch {
	case reference == nil:
		p.reset("noReference")
	default:
		p.reset("waiting")
	}
	w.WriteHeader(http.StatusNoContent)
}
