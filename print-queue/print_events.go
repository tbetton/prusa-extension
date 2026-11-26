// The follower of the host's event stream.
//
// The scheduler does not wait for a printer call to tell it how a print went.
// It subscribes to the host's events (transfer settled, command settled, job
// started, job settled), and each event moves the matching job one step:
//
//	transfer done -> start command requested -> job started -> job settled
//
// Every window of events and the cursor after it are saved in one write, so a
// restart resumes exactly where the last write stopped and misses nothing.
package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

// follow keeps one subscription to the host's events open for as long as the
// module runs, and opens it again, one second later, whenever it ends. Each
// time it resumes from the cursor saved with the jobs. While it is not
// subscribed, `following` is false and no job starts.
func (sc *Scheduler) follow(ctx context.Context, conn *wirerpc.Conn) {
	params := busapi.SubscribeParams{Types: []string{
		event.TypeDeviceCommandRequested, event.TypeDeviceCommandSettled,
		event.TypeDeviceTransferSettled, event.TypeDeviceJobStarted, event.TypeDeviceJobSettled,
	}}

	for ctx.Err() == nil {
		sc.mu.Lock()
		sc.following = true
		sc.store.mu.Lock()
		params.Resume = sc.store.data.Cursor
		sc.store.mu.Unlock()
		sc.mu.Unlock()

		streamCtx, cancel := context.WithCancel(ctx)
		err := busapi.Subscribe(streamCtx, conn, params, func(window busapi.Window) error {
			err := sc.applyWindow(window)
			if err != nil {
				cancel()
			}
			return err
		})
		cancel()

		sc.mu.Lock()
		sc.following = false
		sc.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			sc.logf("follow printer events: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// applyWindow applies one batch of events and saves the new cursor together
// with the jobs it changed. A window marked Resync means events may have been
// lost, so every job in flight is held as unconfirmed for a person to check on
// the printer.
func (sc *Scheduler) applyWindow(window busapi.Window) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	now := sc.now().UTC()
	return sc.store.update(func(data *snapshot) error {
		for _, raw := range window.Events {
			var frame event.Envelope
			if err := json.Unmarshal(raw, &frame); err != nil {
				return err
			}
			applyPrintEvent(data, frame)
		}
		if window.Resync {
			for i := range data.Jobs {
				job := &data.Jobs[i]
				if job.Status == statusStarting || job.Status == statusPrinting {
					holdUnconfirmed(data, job, now)
				}
			}
		}
		data.Cursor = window.Cursor
		return nil
	})
}

// applyPrintEvent moves the jobs of the printer an event came from. An event
// matches a job only through the ids the job already carries (transfer id,
// command id, print id), never by file name alone, so two prints of the same
// file cannot be mixed up. A job held as unconfirmed still accepts the start
// and end events of its print, which is how it recovers by itself.
func applyPrintEvent(data *snapshot, frame event.Envelope) {
	_, origin, err := event.ParseSourceURN(frame.Source)
	if err != nil || origin.Kind() != event.KindDevice {
		return
	}
	for i := range data.Jobs {
		job := &data.Jobs[i]
		if job.DeviceID != origin.ID() {
			continue
		}
		recovering := job.DetailCode == "unconfirmed" &&
			(frame.Type == event.TypeDeviceJobStarted || frame.Type == event.TypeDeviceJobSettled)
		if !recovering && job.Status != statusStarting && job.Status != statusPrinting {
			continue
		}

		switch frame.Type {
		case event.TypeDeviceTransferSettled:
			var transfer event.TransferSettled
			if json.Unmarshal(frame.Data, &transfer) == nil && job.TransferID != "" && job.TransferID == transfer.TransferID {
				if transfer.State == event.TransferDone {
					job.TransferDone = true
				} else {
					job.Status = statusFailed
					job.setDetail("transferFailed")
					job.EndedAt = &frame.Time
				}
			}
		case event.TypeDeviceCommandRequested:
			var command event.CommandRequested
			if json.Unmarshal(frame.Data, &command) == nil && job.TransferDone && job.CommandID == "" &&
				command.Verb == "core.command.start_print" && command.Args["path"] == job.PrinterPath &&
				(job.StartedAt == nil || !frame.Time.Before(*job.StartedAt)) {
				job.CommandID = command.CommandID
			}
		case event.TypeDeviceCommandSettled:
			var command event.CommandSettled
			if json.Unmarshal(frame.Data, &command) == nil && job.CommandID != "" && job.CommandID == command.CommandID {
				switch {
				case command.OutcomeUnknown && job.PrintID == "":
					holdUnconfirmed(data, job, frame.Time)
				case !command.OutcomeUnknown && !command.OK && job.PrintID == "":
					job.Status = statusFailed
					job.setDetail("startRejected")
					job.EndedAt = &frame.Time
				}
			}
		case event.TypeDeviceJobStarted:
			var started event.JobStarted
			if json.Unmarshal(frame.Data, &started) == nil && (job.Status == statusStarting || recovering) &&
				job.CommandID != "" && job.CommandID == started.CommandID && started.JobID != "" &&
				(job.PrintID == "" || job.PrintID == started.JobID) {
				job.PrintID = started.JobID
				job.StartedAt = &started.Started
				markPrinting(job)
			}
		case event.TypeDeviceJobSettled:
			var settled event.JobSettled
			if json.Unmarshal(frame.Data, &settled) == nil && job.PrintID != "" && job.PrintID == settled.JobID {
				settlePrint(data, job, settled.Ended, settled.Outcome)
			}
		}
	}
}

// markPrinting records that the printer has started the job's print.
func markPrinting(job *Job) {
	job.Status = statusPrinting
	job.EndedAt = nil
	job.setDetail("")
}

// settlePrint ends a job with the outcome the printer reported: finished is
// done, cancelled is stopped, failed is failed. Any other outcome is not
// trusted, and the job is held as unconfirmed instead.
func settlePrint(data *snapshot, job *Job, ended time.Time, outcome string) {
	switch outcome {
	case event.JobFinished:
		job.Status = statusDone
		job.setDetail("")
	case event.JobCancelled:
		job.Status = statusStopped
		job.setDetail("")
	case event.JobFailed:
		job.Status = statusFailed
		job.setDetail("printFailed")
	default:
		if job.DetailCode != "unconfirmed" {
			holdUnconfirmed(data, job, ended)
		}
		return
	}
	job.EndedAt = &ended
}
