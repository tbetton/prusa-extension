package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

func (sc *Scheduler) syncPrints(ctx context.Context, deviceID string, now time.Time) {
	candidates := []Job{}
	for _, job := range sc.store.List(deviceID).Jobs {
		if needsPrintSync(job) {
			candidates = append(candidates, job)
		}
	}
	if len(candidates) == 0 {
		return
	}

	recent, err := sc.fleet.Jobs(ctx, deviceapi.JobsParams{ID: deviceID})
	if err != nil {
		sc.logf("read current print on %s: %v", deviceID, err)
		return
	}
	prints := map[string]deviceapi.Job{}
	for _, job := range []*deviceapi.Job{recent.Current, recent.Last} {
		if job != nil && job.ID != "" {
			prints[job.ID] = *job
		}
	}

	from := now
	readHistory := false
	for _, job := range candidates {
		if _, found := prints[job.PrintID]; found {
			continue
		}
		readHistory = true
		if job.StartedAt == nil {
			from = time.Time{}
		} else if job.StartedAt.Before(from) {
			from = *job.StartedAt
		}
	}
	var historyErr error
	if readHistory {
		historyErr = sc.readPrintHistory(ctx, deviceID, from, prints)
		if historyErr != nil {
			sc.logf("read print history on %s: %v", deviceID, historyErr)
		}
	}

	if err := sc.store.update(func(data *snapshot) error {
		for i := range data.Jobs {
			job := &data.Jobs[i]
			if job.DeviceID != deviceID || !needsPrintSync(*job) {
				continue
			}
			print, found := prints[job.PrintID]
			if job.PrintID == "" && readHistory && historyErr == nil {
				print, found = uniquePrint(data.Jobs, i, prints, now)
			}
			if !found {
				continue
			}
			job.PrintID = print.ID
			if !print.Started.IsZero() {
				job.StartedAt = &print.Started
			}
			if print.Ended == nil {
				markPrinting(job)
				continue
			}
			settlePrint(data, job, *print.Ended, print.Outcome)
		}
		return nil
	}); err != nil {
		sc.logf("save synced prints on %s: %v", deviceID, err)
	}
}

func (sc *Scheduler) readPrintHistory(ctx context.Context, deviceID string, from time.Time, prints map[string]deviceapi.Job) error {
	params := deviceapi.JobHistoryParams{ID: deviceID, From: from, Limit: deviceapi.MaxPageLimit}
	for {
		page, err := sc.fleet.JobHistory(ctx, params)
		if err != nil {
			return err
		}
		for _, print := range page.Items {
			if print.ID != "" && print.Ended != nil {
				prints[print.ID] = print
			}
		}
		if page.Cursor == "" {
			return nil
		}
		if page.Cursor == params.Cursor {
			return fmt.Errorf("print history repeated its cursor")
		}
		params.Cursor = page.Cursor
	}
}

func needsPrintSync(job Job) bool {
	return (job.Status == statusStarting || job.Status == statusPrinting || job.DetailCode == "unconfirmed") &&
		(job.PrintID != "" || job.StartedAt != nil)
}

// Without a print ID, the exact path and start window must identify one
// print and one queue attempt. A later repeat of the same file is unrelated.
func uniquePrint(jobs []Job, index int, prints map[string]deviceapi.Job, now time.Time) (deviceapi.Job, bool) {
	var match deviceapi.Job
	for _, print := range prints {
		if !samePrintAttempt(jobs[index], print, now) {
			continue
		}
		if match.ID != "" {
			return deviceapi.Job{}, false
		}
		match = print
	}
	if match.ID == "" {
		return deviceapi.Job{}, false
	}
	for i, job := range jobs {
		if i != index && job.DeviceID == jobs[index].DeviceID &&
			(job.PrintID == match.ID || (needsPrintSync(job) && job.PrintID == "" && samePrintAttempt(job, match, now))) {
			return deviceapi.Job{}, false
		}
	}
	return match, true
}

func samePrintAttempt(job Job, print deviceapi.Job, now time.Time) bool {
	if job.StartedAt == nil || print.Started.IsZero() || print.Started.Before(*job.StartedAt) {
		return false
	}
	latest := now
	if job.EndedAt != nil {
		latest = *job.EndedAt
	}
	if print.Started.After(latest) {
		return false
	}
	printerPath := job.PrinterPath
	if printerPath == "" && job.Storage == "" {
		printerPath = job.Path
	}
	return printerPath != "" && strings.TrimPrefix(printerPath, "/") == strings.TrimPrefix(print.Path, "/")
}
