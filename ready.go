package main

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

func (sc *Scheduler) sendReady(ctx context.Context, id string) error {
	result, err := sc.command(ctx, deviceapi.CommandParams{ID: id, Verb: readyVerb})
	if err != nil {
		return errors.New("could not confirm the READY command")
	}
	if !result.OK || len(result.Ignored) > 0 || len(result.Substituted) > 0 {
		return errors.New("the printer did not accept the READY command")
	}
	return nil
}

func (m *module) markReady(w http.ResponseWriter, r *http.Request) {
	if m.ready(w, r) == nil {
		return
	}
	id := r.PathValue("id")
	if !m.requireOperate(w, r, id) {
		return
	}
	var body struct {
		PlateEmpty bool `json:"plate_empty"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.PlateEmpty {
		refuse(w, r, BadRequest.New("confirm that the build plate is empty"))
		return
	}
	sc := m.scheduler
	if sc == nil {
		refuse(w, r, NotReady.New("the queue is starting"))
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()

	device, err := sc.fleet.Get(r.Context(), deviceapi.GetParams{ID: id})
	if err != nil || device.Retired || (device.Kind != "" && device.Kind != "printer") {
		refuse(w, r, NoDevice.New("the printer is unavailable"))
		return
	}
	state := printerState(device)
	if !platePrinterAvailable(device) || !slices.Contains(device.Capabilities, readyVerb) ||
		(state != "IDLE" && state != "FINISHED" && state != "STOPPED") {
		refuse(w, r, ReadyUnavailable.New("the printer must be connected and idle, finished or stopped"))
		return
	}
	for _, job := range sc.store.List(id).Jobs {
		if job.Status == statusStarting || job.Status == statusPrinting {
			refuse(w, r, ReadyUnavailable.New("Print Queue is starting or tracking another print"))
			return
		}
	}
	if _, err := sc.lastPlatePrint(r.Context(), id); err != nil {
		refuse(w, r, ReadyUnavailable.New("could not confirm that there is no current print"))
		return
	}
	if err := sc.sendReady(r.Context(), id); err != nil {
		refuse(w, r, ReadyUnavailable.New(err.Error()))
		return
	}
	// An operator confirmed the plate after an earlier automatic attempt.
	// The scheduler can use the new READY once it sees the printer report it.
	if err := sc.store.update(func(data *snapshot) error {
		delete(data.PlateReady, id)
		return nil
	}); err != nil {
		refuse(w, r, StoreFailed.New("the printer accepted READY but the queue could not save the confirmation"))
		return
	}
	if p := sc.plate; p != nil && p.settings.Device == id {
		p.permit = platePermit{}
		if p.reference != nil {
			p.reset("waiting")
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
