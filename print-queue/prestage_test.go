package main

import (
	"context"
	"errors"
	"path"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
)

func TestTheNextFileIsCopiedAheadAndPrintedWhereItIs(t *testing.T) {
	store, fleet, sc, c := setup(t, "IDLE")
	sc.prestage = true

	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "a/one.bgcode"})

	sc.Tick(context.Background())

	if len(fleet.sent) != 1 || fleet.sent[0].Source != "a/one.bgcode" || fleet.sent[0].Storage != "local" ||
		path.Dir(fleet.sent[0].Path) != "usb" {
		t.Fatalf("sent %+v, want a/one.bgcode copied to usb", fleet.sent)
	}

	job := status(t, store, a.ID)
	if job.Status != statusQueued || job.StagedPath != fleet.sent[0].Path || job.StageTransferID != "s1" || job.Staged {
		t.Fatalf("unexpected job after the copy was asked: %+v", job)
	}

	// The copy is under way: it is not asked for twice, and nothing prints.
	sc.Tick(context.Background())

	if len(fleet.sent) != 1 || len(fleet.printed) != 0 {
		t.Fatalf("sent %d, printed %d, want 1 and 0", len(fleet.sent), len(fleet.printed))
	}

	pushEvents(t, sc, c.at, frame(t, event.TypeDeviceTransferSettled, event.TransferSettled{TransferID: "s1", State: event.TransferDone}))

	job = status(t, store, a.ID)
	if !job.Staged || job.StageTransferID != "" || job.Status != statusQueued {
		t.Fatalf("the copy should have landed: %+v", job)
	}

	// The printer becomes ready: the copy on the printer is printed at once.
	fleet.state = printerReady
	sc.Tick(context.Background())

	if len(fleet.printed) != 1 || fleet.printed[0].Storage != "" || fleet.printed[0].Path != job.StagedPath {
		t.Fatalf("printed %+v, want the copy at %s with nothing to send", fleet.printed, job.StagedPath)
	}

	if got := status(t, store, a.ID); got.Status != statusStarting || got.PrinterPath != job.StagedPath || got.CommandID != "c1" {
		t.Fatalf("unexpected job after the start: %+v", got)
	}
}

func TestAJobWaitsForItsCopyToLand(t *testing.T) {
	store, fleet, sc, c := setup(t, "IDLE")
	sc.prestage = true

	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})

	sc.Tick(context.Background())

	// READY arrives while the copy still travels: the file is not sent twice.
	fleet.state = printerReady
	sc.Tick(context.Background())

	if len(fleet.printed) != 0 {
		t.Fatalf("a print started while the copy travelled: %+v", fleet.printed)
	}

	pushEvents(t, sc, c.at, frame(t, event.TypeDeviceTransferSettled, event.TransferSettled{TransferID: "s1", State: event.TransferDone}))
	sc.Tick(context.Background())

	if len(fleet.printed) != 1 || fleet.printed[0].Storage != "" || fleet.printed[0].Path != status(t, store, a.ID).PrinterPath {
		t.Fatalf("printed %+v, want the landed copy", fleet.printed)
	}
}

func TestAFailedCopyFallsBackToTheNormalStart(t *testing.T) {
	store, fleet, sc, c := setup(t, "IDLE")
	sc.prestage = true
	fleet.sendErr = errors.New("the printer is busy")

	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})

	sc.Tick(context.Background())

	if job := status(t, store, a.ID); job.StagedPath != "" || job.StageTransferID != "" || job.Staged {
		t.Fatalf("a failed copy leaves no trace: %+v", job)
	}

	// Within the minute it is not tried again, then it is.
	fleet.sendErr = nil

	sc.Tick(context.Background())

	if len(fleet.sent) != 0 {
		t.Fatalf("the copy was tried again at once: %+v", fleet.sent)
	}

	c.at = c.at.Add(2 * time.Minute)
	sc.Tick(context.Background())

	if len(fleet.sent) != 1 {
		t.Fatalf("the copy should be tried again after a minute: %+v", fleet.sent)
	}

	// A copy that fails on arrival is forgotten too.
	pushEvents(t, sc, c.at, frame(t, event.TypeDeviceTransferSettled, event.TransferSettled{TransferID: "s1", State: event.TransferFailed}))

	if job := status(t, store, a.ID); job.StagedPath != "" || job.Staged || job.StageTransferID != "" {
		t.Fatalf("a failed transfer is forgotten: %+v", job)
	}

	// With the printer ready, the file is sent and started as before.
	fleet.state = printerReady
	sc.Tick(context.Background())

	if len(fleet.printed) != 1 || fleet.printed[0].Storage != "local" || fleet.printed[0].Source != "one.bgcode" {
		t.Fatalf("printed %+v, want the usual send and start", fleet.printed)
	}
}

func TestAMissingCopyIsSentAgain(t *testing.T) {
	store, fleet, sc, _ := setup(t, printerReady)

	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})
	if err := store.update(func(data *snapshot) error {
		data.Jobs[0].StagedPath = "usb/gone-one.bgcode"
		data.Jobs[0].Staged = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fleet.printErr = deviceapi.ErrNoFile
	sc.Tick(context.Background())

	if job := status(t, store, a.ID); job.Status != statusQueued || job.Staged || job.StagedPath != "" || job.PrinterPath != "" {
		t.Fatalf("a copy that is gone sends the file again: %+v", job)
	}

	fleet.printErr = nil
	sc.Tick(context.Background())

	if last := fleet.printed[len(fleet.printed)-1]; last.Storage != "local" || last.Source != "one.bgcode" {
		t.Fatalf("printed %+v, want the usual send and start", fleet.printed)
	}
}

func TestAStaleCopyIsForgottenAndPrestageCanBeOff(t *testing.T) {
	store, fleet, sc, c := setup(t, "IDLE")

	a, _ := store.Add(Job{DeviceID: "p1", Storage: "local", Path: "one.bgcode"})

	// Prestage is off: nothing is copied.
	sc.Tick(context.Background())

	if len(fleet.sent) != 0 {
		t.Fatalf("prestage is off, but %+v was sent", fleet.sent)
	}

	started := c.at.Add(-11 * time.Minute)
	if err := store.update(func(data *snapshot) error {
		data.Jobs[0].StagedPath = "usb/x-one.bgcode"
		data.Jobs[0].StageTransferID = "lost"
		data.Jobs[0].StagedAt = &started

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sc.Tick(context.Background())

	if job := status(t, store, a.ID); job.StagedPath != "" || job.StageTransferID != "" || job.StagedAt != nil {
		t.Fatalf("a copy that never settled is forgotten: %+v", job)
	}
}

func TestOnlyTheNextFileIsCopiedAndAtMostTwoStayOnThePrinter(t *testing.T) {
	store, fleet, sc, c := setup(t, "IDLE")
	sc.prestage = true

	// A file already on the printer needs no copy.
	held, _ := store.Add(Job{DeviceID: "p1", Storage: "", Path: "usb/held.bgcode"})
	sc.Tick(context.Background())

	if len(fleet.sent) != 0 {
		t.Fatalf("a file on the printer was copied: %+v", fleet.sent)
	}

	if err := store.Remove(held.ID); err != nil {
		t.Fatal(err)
	}

	var jobs []Job

	for _, name := range []string{"1.bgcode", "2.bgcode", "3.bgcode"} {
		job, err := store.Add(Job{DeviceID: "p1", Storage: "local", Path: name})
		if err != nil {
			t.Fatal(err)
		}

		jobs = append(jobs, job)
	}

	land := func() {
		t.Helper()

		for _, job := range store.List("p1").Jobs {
			if job.StageTransferID != "" {
				pushEvents(t, sc, c.at, frame(t, event.TypeDeviceTransferSettled, event.TransferSettled{TransferID: job.StageTransferID, State: event.TransferDone}))
			}
		}
	}

	// Only the next file is copied, however long the queue is.
	for range 3 {
		sc.Tick(context.Background())
		land()
	}

	if len(fleet.sent) != 1 || fleet.sent[0].Source != "1.bgcode" {
		t.Fatalf("sent %+v, want only the first file", fleet.sent)
	}

	// A file moved to the front is copied too, so two files are on the printer.
	for range 2 {
		if err := store.Move(jobs[2].ID, true); err != nil {
			t.Fatal(err)
		}
	}

	sc.Tick(context.Background())
	land()

	if len(fleet.sent) != 2 || fleet.sent[1].Source != "3.bgcode" {
		t.Fatalf("sent %+v, want the moved file as the second", fleet.sent)
	}

	// A third one would be one too many.
	for range 2 {
		if err := store.Move(jobs[1].ID, true); err != nil {
			t.Fatal(err)
		}
	}

	sc.Tick(context.Background())
	land()

	if len(fleet.sent) != maxStaged {
		t.Fatalf("copied %d files, want at most %d", len(fleet.sent), maxStaged)
	}
}
