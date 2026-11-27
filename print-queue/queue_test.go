package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
)

type fakeFleet struct {
	state     string
	connected bool
	printed   []deviceapi.PrintParams
	sent      []deviceapi.SendParams
	sendErr   error
	printErr  error
	recent    deviceapi.Jobs
	history   func(deviceapi.JobHistoryParams) (deviceapi.JobPage, error)
}

func (f *fakeFleet) Get(_ context.Context, params deviceapi.GetParams) (deviceapi.Device, error) {
	raw, _ := json.Marshal(f.state)

	return deviceapi.Device{ID: params.ID, Connected: f.connected, State: map[string]json.RawMessage{"/state": raw}}, nil
}

func (f *fakeFleet) Print(_ context.Context, params deviceapi.PrintParams) (deviceapi.Started, error) {
	f.printed = append(f.printed, params)

	if f.printErr != nil {
		return deviceapi.Started{}, f.printErr
	}

	if params.Storage == "" {
		return deviceapi.Started{CommandID: "c1"}, nil
	}
	return deviceapi.Started{TransferID: "t1"}, nil
}

func (f *fakeFleet) Send(_ context.Context, params deviceapi.SendParams) (deviceapi.Sent, error) {
	if f.sendErr != nil {
		return deviceapi.Sent{}, f.sendErr
	}

	f.sent = append(f.sent, params)

	return deviceapi.Sent{TransferID: "s1"}, nil
}

func (f *fakeFleet) Jobs(_ context.Context, _ deviceapi.JobsParams) (deviceapi.Jobs, error) {
	return f.recent, nil
}

func (f *fakeFleet) JobHistory(_ context.Context, params deviceapi.JobHistoryParams) (deviceapi.JobPage, error) {
	if f.history == nil {
		return deviceapi.JobPage{}, nil
	}
	return f.history(params)
}

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func setup(t *testing.T, state string) (*Store, *fakeFleet, *Scheduler, *clock) {
	t.Helper()

	c := &clock{at: time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)}

	store, err := OpenStore(t.TempDir(), c.now, 20)
	if err != nil {
		t.Fatal(err)
	}

	fleet := &fakeFleet{state: state, connected: true}
	sc := &Scheduler{store: store, fleet: fleet, now: c.now, startTimeout: 30 * time.Minute, folder: "usb", logf: t.Logf, following: true, authorize: func(context.Context, Job) error { return nil }}

	return store, fleet, sc, c
}

func status(t *testing.T, store *Store, id string) Job {
	t.Helper()

	for _, job := range store.List("").Jobs {
		if job.ID == id {
			return job
		}
	}

	t.Fatalf("job %s is gone", id)

	return Job{}
}

func TestTheFirstJobStartsWhenThePrinterIsReady(t *testing.T) {
	store, fleet, sc, c := setup(t, printerReady)

	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "a/one.bgcode"})
	b, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "two.bgcode"})

	sc.Tick(context.Background())

	if len(fleet.printed) != 1 || fleet.printed[0].Source != "a/one.bgcode" || path.Dir(fleet.printed[0].Path) != "usb" || !strings.HasSuffix(fleet.printed[0].Path, "-one.bgcode") {
		t.Fatalf("printed %+v, want a/one.bgcode to usb/one.bgcode", fleet.printed)
	}

	if got := status(t, store, a.ID).Status; got != statusStarting {
		t.Fatalf("first job is %s, want starting", got)
	}

	// Still READY while the file travels: nothing else starts.
	sc.Tick(context.Background())

	if len(fleet.printed) != 1 {
		t.Fatalf("a second job started during the transfer: %+v", fleet.printed)
	}

	fleet.state = "PRINTING"
	pushEvents(t, sc, c.at,
		frame(t, event.TypeDeviceJobStarted, event.JobStarted{JobID: "unrelated", Started: c.at}),
		frame(t, event.TypeDeviceTransferSettled, event.TransferSettled{TransferID: "t1", State: event.TransferDone}),
		frame(t, event.TypeDeviceCommandRequested, event.CommandRequested{CommandID: "c1", Verb: "core.command.start_print", Args: map[string]string{"path": fleet.printed[0].Path}}),
		frame(t, event.TypeDeviceCommandSettled, event.CommandSettled{CommandID: "c1", OK: true}),
		frame(t, event.TypeDeviceJobStarted, event.JobStarted{JobID: "print1", CommandID: "c1", Started: c.at}),
	)

	if got := status(t, store, a.ID).Status; got != statusPrinting {
		t.Fatalf("first job is %s, want printing", got)
	}

	pushEvents(t, sc, c.at, frame(t, event.TypeDeviceJobSettled, event.JobSettled{JobID: "unrelated", Outcome: event.JobFinished, Ended: c.at}))
	if got := status(t, store, a.ID).Status; got != statusPrinting {
		t.Fatalf("another print changed the job to %s", got)
	}
	fleet.state = "FINISHED"
	pushEvents(t, sc, c.at, frame(t, event.TypeDeviceJobSettled, event.JobSettled{JobID: "print1", Outcome: event.JobFinished, Ended: c.at}))

	if got := status(t, store, a.ID).Status; got != statusDone {
		t.Fatalf("first job is %s, want done", got)
	}

	// FINISHED is not READY: the plate has not been cleared yet.
	sc.Tick(context.Background())

	if len(fleet.printed) != 1 {
		t.Fatalf("the next job started before READY")
	}

	fleet.state = printerReady
	sc.Tick(context.Background())

	if got := status(t, store, b.ID).Status; got != statusStarting {
		t.Fatalf("second job is %s, want starting", got)
	}
}

func TestAScheduledJobWaitsAndTheNextOneGoesFirst(t *testing.T) {
	store, fleet, sc, c := setup(t, printerReady)

	later := c.at.Add(2 * time.Hour)
	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "night.bgcode", NotBefore: &later})
	b, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "now.bgcode"})

	sc.Tick(context.Background())

	if len(fleet.printed) != 1 || fleet.printed[0].Source != "now.bgcode" {
		t.Fatalf("printed %+v, want now.bgcode", fleet.printed)
	}

	if status(t, store, b.ID).Status != statusStarting || status(t, store, a.ID).Status != statusQueued {
		t.Fatal("the scheduled job did not wait")
	}
}

func TestAScheduledJobStartsAtItsTime(t *testing.T) {
	store, fleet, sc, c := setup(t, printerReady)

	later := c.at.Add(time.Hour)
	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "night.bgcode", NotBefore: &later})

	sc.Tick(context.Background())

	if len(fleet.printed) != 0 {
		t.Fatal("started before its time")
	}

	c.at = later.Add(time.Second)
	sc.Tick(context.Background())

	if status(t, store, a.ID).Status != statusStarting {
		t.Fatal("did not start at its time")
	}
}

func TestAPausedQueueStartsNothing(t *testing.T) {
	store, fleet, sc, _ := setup(t, printerReady)

	_, _ = store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})
	_ = store.SetPaused("p1", true)

	sc.Tick(context.Background())

	if len(fleet.printed) != 0 {
		t.Fatal("a paused queue started a job")
	}
}

func TestARevokedPermissionPreventsDispatch(t *testing.T) {
	store, fleet, sc, _ := setup(t, printerReady)
	sc.authorize = func(context.Context, Job) error { return errOperationDenied }

	job, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode", AddedBy: "user:operator"})
	sc.Tick(t.Context())

	if len(fleet.printed) != 0 || status(t, store, job.ID).Status != statusFailed {
		t.Fatal("a revoked grant did not hold the print")
	}
}

func TestAJobThatNeverStartsFails(t *testing.T) {
	store, _, sc, c := setup(t, printerReady)

	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})
	sc.Tick(context.Background())

	c.at = c.at.Add(31 * time.Minute)
	sc.Tick(context.Background())

	if got := status(t, store, a.ID).Status; got != statusFailed {
		t.Fatalf("job is %s, want failed", got)
	}
}

func TestJobsRecoversAnUnconfirmedPrintAndItsActualFinishTime(t *testing.T) {
	store, fleet, sc, c := setup(t, printerReady)
	queued, _ := store.Add(Job{DeviceID: "p1", Path: "usb/plate.bgcode"})
	next, _ := store.Add(Job{DeviceID: "p1", Path: "usb/next.bgcode"})
	sc.Tick(t.Context())

	started := c.at.Add(time.Second)
	c.at = c.at.Add(3 * time.Minute)
	if err := sc.applyWindow(busapi.Window{Resync: true}); err != nil {
		t.Fatal(err)
	}
	if job := status(t, store, queued.ID); job.DetailCode != "unconfirmed" || job.PrintID != "" {
		t.Fatalf("before recovery: %+v", job)
	}

	fleet.state = "PRINTING"
	fleet.recent.Current = &deviceapi.Job{ID: "print-1", Path: "/usb/plate.bgcode", Started: started}
	sc.following = false
	sc.Tick(t.Context())
	if job := status(t, store, queued.ID); job.Status != statusPrinting || job.PrintID != "print-1" ||
		job.DetailCode != "" || job.EndedAt != nil || job.StartedAt == nil || !job.StartedAt.Equal(started) {
		t.Fatalf("current print was not recovered: %+v", job)
	}

	ended := started.Add(time.Hour)
	c.at = ended.Add(time.Minute)
	sc.following = true
	fleet.state = printerReady
	fleet.recent = deviceapi.Jobs{}
	fleet.history = func(p deviceapi.JobHistoryParams) (deviceapi.JobPage, error) {
		if p.ID != "p1" || !p.From.Equal(started) || p.Principal != "" {
			t.Fatalf("history query: %+v", p)
		}
		switch p.Cursor {
		case "":
			return deviceapi.JobPage{Cursor: "next"}, nil
		case "next":
			return deviceapi.JobPage{Items: []deviceapi.Job{{
				ID: "print-1", Started: started, Ended: &ended, Outcome: event.JobFinished,
			}}}, nil
		default:
			t.Fatalf("unexpected history cursor: %q", p.Cursor)
			return deviceapi.JobPage{}, nil
		}
	}
	sc.Tick(t.Context())
	reopened, err := OpenStore(path.Dir(store.file), c.now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if job := status(t, reopened, queued.ID); job.Status != statusDone || job.Detail != "" || job.EndedAt == nil || !job.EndedAt.Equal(ended) {
		t.Fatalf("finished print was not recovered and saved: %+v", job)
	}
	if !reopened.List("p1").Paused["p1"] || status(t, reopened, next.ID).Status != statusQueued || len(fleet.printed) != 1 {
		t.Fatal("the queue resumed without operator confirmation")
	}
}

func TestHistoryOnlyRecoversAnUnambiguousPrintFromTheSameAttempt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offsets []time.Duration
		want    string
	}{
		{name: "one match in the window", offsets: []time.Duration{time.Second}, want: statusDone},
		{name: "same path printed another time", offsets: []time.Duration{5 * time.Minute}, want: statusFailed},
		{name: "multiple matches in the window", offsets: []time.Duration{time.Second, time.Minute}, want: statusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, fleet, sc, c := setup(t, printerReady)
			queued, _ := store.Add(Job{DeviceID: "p1", Path: "usb/plate.bgcode"})
			sc.Tick(t.Context())
			dispatched := c.at
			c.at = c.at.Add(3 * time.Minute)
			if err := sc.applyWindow(busapi.Window{Resync: true}); err != nil {
				t.Fatal(err)
			}
			ended := c.at.Add(time.Hour)
			c.at = ended
			fleet.history = func(_ deviceapi.JobHistoryParams) (deviceapi.JobPage, error) {
				var page deviceapi.JobPage
				for _, offset := range tc.offsets {
					page.Items = append(page.Items, deviceapi.Job{
						ID: offset.String(), Path: "usb/plate.bgcode", Started: dispatched.Add(offset),
						Ended: &ended, Outcome: event.JobFinished,
					})
				}
				return page, nil
			}
			sc.Tick(t.Context())
			if job := status(t, store, queued.ID); job.Status != tc.want {
				t.Fatalf("history changed job to %+v, want %s", job, tc.want)
			}
		})
	}
}

func TestMoveAndReload(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Unix(0, 0) }

	store, _ := OpenStore(dir, now, 20)
	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "a.bgcode"})
	_, _ = store.Add(Job{DeviceID: "p2", Storage: "local", Path: "x.bgcode"})
	b, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "b.bgcode"})

	if err := store.Move(b.ID, true); err != nil {
		t.Fatal(err)
	}

	again, err := OpenStore(dir, now, 20)
	if err != nil {
		t.Fatal(err)
	}

	jobs := again.List("p1").Jobs
	if len(jobs) != 2 || jobs[0].ID != b.ID || jobs[1].ID != a.ID {
		t.Fatalf("order after reload is %+v", jobs)
	}
}

func TestAFailedTransferFailsOnlyItsJob(t *testing.T) {
	store, _, sc, c := setup(t, printerReady)
	job, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})
	sc.Tick(t.Context())

	pushEvents(t, sc, c.at, frame(t, event.TypeDeviceTransferSettled, event.TransferSettled{TransferID: "other", State: event.TransferFailed}))
	if got := status(t, store, job.ID).Status; got != statusStarting {
		t.Fatalf("unrelated transfer changed job to %s", got)
	}

	pushEvents(t, sc, c.at, frame(t, event.TypeDeviceTransferSettled, event.TransferSettled{TransferID: "t1", State: event.TransferFailed}))
	if got := status(t, store, job.ID).Status; got != statusFailed {
		t.Fatalf("job is %s, want failed", got)
	}
}

func TestPersistenceErrorLeavesTheQueueUnchanged(t *testing.T) {
	store, fleet, sc, _ := setup(t, printerReady)
	job, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})
	store.file = path.Join(t.TempDir(), "missing", "queue.json")
	if err := store.SetPaused("p1", true); err == nil {
		t.Fatal("expected a persistence error")
	}

	sc.Tick(t.Context())
	view := store.List("p1")
	if view.Paused["p1"] || status(t, store, job.ID).Status != statusQueued || len(fleet.printed) != 0 {
		t.Fatal("a failed write changed the queue or started an impression")
	}
}

func TestUnknownAuthorizationCanBeRetried(t *testing.T) {
	store, fleet, sc, _ := setup(t, printerReady)
	sc.authorize = func(context.Context, Job) error { return errors.Join(context.DeadlineExceeded) }
	job, _ := store.Add(Job{DeviceID: "p1", Path: "usb/one.bgcode"})
	sc.Tick(t.Context())
	if got := status(t, store, job.ID).Status; got != statusQueued || len(fleet.printed) != 0 {
		t.Fatalf("job is %s, want queued", got)
	}
}

func frame(t *testing.T, kind string, data any) event.Envelope {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return event.Envelope{Type: kind, Source: "urn:prusa:site:local/device/p1", Data: raw}
}

func pushEvents(t *testing.T, sc *Scheduler, now time.Time, events ...event.Envelope) {
	t.Helper()
	window := busapi.Window{}
	for _, frame := range events {
		frame.Time = now
		raw, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		window.Events = append(window.Events, raw)
	}
	if err := sc.applyWindow(window); err != nil {
		t.Fatal(err)
	}
}

func TestAFileOnThePrinterStartsWhereItIs(t *testing.T) {
	store, fleet, sc, _ := setup(t, printerReady)

	_, _ = store.Add(Job{DeviceID: "p1", Path: "usb/plate.bgcode"})
	sc.Tick(context.Background())

	if len(fleet.printed) != 1 || fleet.printed[0].Storage != "" || fleet.printed[0].Path != "usb/plate.bgcode" {
		t.Fatalf("printed %+v, want usb/plate.bgcode from the printer", fleet.printed)
	}
}

func TestLegacyJobDetailsAreRestoredInEnglish(t *testing.T) {
	dir := t.TempDir()
	old := []byte(`{"jobs":[
		{"id":"known","device_id":"p1","status":"failed","detail":"Le transfert vers l’imprimante a échoué."},
		{"id":"coded","device_id":"p1","status":"failed","detail":"L’impression a échoué.","detail_code":"printFailed"},
		{"id":"custom","device_id":"p1","status":"failed","detail":"Printer diagnostic"}
	]}`)
	if err := os.WriteFile(filepath.Join(dir, "queue.json"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, time.Now, 20)
	if err != nil {
		t.Fatal(err)
	}
	known := status(t, store, "known")
	if known.DetailCode != "transferFailed" || known.Detail != "The transfer to the printer failed." {
		t.Fatalf("known failure = %+v", known)
	}
	coded := status(t, store, "coded")
	if coded.DetailCode != "printFailed" || coded.Detail != "The print failed." {
		t.Fatalf("coded failure = %+v", coded)
	}
	custom := status(t, store, "custom")
	if custom.DetailCode != "" || custom.Detail != "Printer diagnostic" {
		t.Fatalf("custom detail = %+v", custom)
	}
	if err := store.Requeue(known.ID, "user:operator"); err != nil {
		t.Fatal(err)
	}
	if queued := status(t, store, known.ID); queued.DetailCode != "" || queued.Detail != "" {
		t.Fatalf("requeued job still has a failure detail: %+v", queued)
	}
}
