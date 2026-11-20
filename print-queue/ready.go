package main

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

const readyVerb = "core.command.set_printer_ready"

// Retain the old plate_ready marker so upgrading cannot dispatch a job from
// an unconfirmed READY command made by an earlier release.
type readyMarker struct {
	Seen bool `json:"seen"`
}

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
	queue := sc.store.List(id)
	confirmingReady := state == printerReady && queue.ReadyConfirmationRequired[id]
	canSetReady := slices.Contains(device.Capabilities, readyVerb) &&
		(state == "IDLE" || state == "FINISHED" || state == "STOPPED")
	if !device.Connected || device.Dialog != nil || len(device.Issues) != 0 || !(confirmingReady || canSetReady) {
		refuse(w, r, ReadyUnavailable.New("the printer must be connected and idle, finished or stopped"))
		return
	}
	for _, job := range queue.Jobs {
		if job.Status == statusStarting || job.Status == statusPrinting {
			refuse(w, r, ReadyUnavailable.New("Print Queue is starting or tracking another print"))
			return
		}
	}
	jobs, err := sc.fleet.Jobs(r.Context(), deviceapi.JobsParams{ID: id})
	if err != nil || (jobs.Current != nil && jobs.Current.Ended == nil) {
		refuse(w, r, ReadyUnavailable.New("could not confirm that there is no current print"))
		return
	}
	if !confirmingReady {
		if err := sc.sendReady(r.Context(), id); err != nil {
			refuse(w, r, ReadyUnavailable.New(err.Error()))
			return
		}
	}
	// An operator confirmed the plate after an earlier automatic attempt.
	// The scheduler can use the new READY once it sees the printer report it.
	if queue.ReadyConfirmationRequired[id] {
		if err := sc.store.update(func(data *snapshot) error {
			delete(data.UnconfirmedReady, id)
			return nil
		}); err != nil {
			refuse(w, r, StoreFailed.New("the queue could not save the readiness confirmation"))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
