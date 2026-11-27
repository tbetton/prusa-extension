package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"
)

func TestFolderLabelAndMatch(t *testing.T) {
	if got := folderLabel(deviceapi.Device{ID: "p1", Name: " Core One: left/right "}); got != "Core One- left-right" {
		t.Fatalf("unexpected label %q", got)
	}

	if got := folderLabel(deviceapi.Device{ID: "p1"}); got != "p1" {
		t.Fatalf("a printer with no name uses its id, got %q", got)
	}

	if !sameFolder("core  ONE", "Core One", "p1") || !sameFolder("p1", "Core One", "p1") || sameFolder("MK4", "Core One", "p1") {
		t.Fatal("folders are matched by name apart from case and spacing, or by id")
	}

	for name, want := range map[string]bool{
		"a.bgcode": true, "B.GCODE": true, "c.gco": true, "d.g": true, "e.txt": false, "f": false,
	} {
		if printableFile(name) != want {
			t.Fatalf("printableFile(%q) should be %v", name, want)
		}
	}
}

func TestInboxScan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-time.Hour).Unix()

	files := []storageapi.ListEntry{
		{Name: "older.bgcode", Size: 10, MTime: old - 100},
		{Name: "newer.gcode", Size: 10, MTime: old},
		{Name: "notes.txt", Size: 10, MTime: old},
		{Name: "empty.bgcode", Size: 0, MTime: old},
		{Name: "sending.bgcode", Size: 10, MTime: now.Add(-time.Second).Unix()},
		{Name: "sub", Dir: true, MTime: old},
	}

	m := &module{
		inboxStorage: "local",
		inboxFolder:  "print-queue",
		list: func(_ context.Context, _, storage, folder string) ([]storageapi.ListEntry, error) {
			if storage != "local" {
				return nil, errors.New("no such storage")
			}

			switch folder {
			case "print-queue":
				return []storageapi.ListEntry{{Name: "core one", Dir: true}, {Name: "mk4", Dir: true}}, nil
			case "print-queue/core one":
				return files, nil
			}

			return nil, errors.New("no such folder")
		},
	}

	store, err := OpenStore("", time.Now, 5)
	if err != nil {
		t.Fatal(err)
	}

	device := deviceapi.Device{ID: "p1", Name: "Core One"}

	view, err := m.inboxScan(context.Background(), "user:operator", device, store, now)
	if err != nil {
		t.Fatal(err)
	}

	if view.Folder != "print-queue/core one" || len(view.Items) != 2 ||
		view.Items[0].Name != "newer.gcode" || view.Items[1].Name != "older.bgcode" {
		t.Fatalf("unexpected inbox: %+v", view)
	}

	if view.Items[0].Path != "print-queue/core one/newer.gcode" {
		t.Fatalf("unexpected path %q", view.Items[0].Path)
	}

	if err = store.MarkHandled("local:"+view.Items[0].Path, view.Items[0].Modified.Unix()); err != nil {
		t.Fatal(err)
	}

	view, err = m.inboxScan(context.Background(), "user:operator", device, store, now)
	if err != nil || len(view.Items) != 1 || view.Items[0].Name != "older.bgcode" {
		t.Fatalf("a handled file is not offered again: %+v %v", view, err)
	}

	// The same name saved again is a new file.
	files[1].MTime = old + 50

	view, err = m.inboxScan(context.Background(), "user:operator", device, store, now)
	if err != nil || len(view.Items) != 2 {
		t.Fatalf("a file saved again is offered again: %+v %v", view, err)
	}

	// A printer with no folder of its own has an empty inbox.
	view, err = m.inboxScan(context.Background(), "user:operator", deviceapi.Device{ID: "p9", Name: "Other"}, store, now)
	if err != nil || len(view.Items) != 0 || view.Folder != "print-queue/Other" {
		t.Fatalf("unexpected inbox for a printer with no folder: %+v %v", view, err)
	}
}

func TestInboxMarksAndAutoAdd(t *testing.T) {
	dir := t.TempDir()

	store, err := OpenStore(dir, time.Now, 5)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.SetAutoAdd("p1", "user:operator", true); err != nil {
		t.Fatal(err)
	}

	if who, ok := store.AutoAddOf("p1"); !ok || who != "user:operator" {
		t.Fatalf("unexpected auto add: %q %v", who, ok)
	}

	if err = store.MarkHandled("local:q/p1/a.bgcode", 5); err != nil {
		t.Fatal(err)
	}

	if err = store.MarkHandled("local:q/p2/b.bgcode", 6); err != nil {
		t.Fatal(err)
	}

	if err = store.ForgetHandled("local:q/p1/", map[string]bool{}); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.HandledMTime("local:q/p1/a.bgcode"); ok {
		t.Fatal("the mark of a file that left its folder is dropped")
	}

	if _, ok := store.HandledMTime("local:q/p2/b.bgcode"); !ok {
		t.Fatal("the mark of another printer's file stays")
	}

	reopened, err := OpenStore(dir, time.Now, 5)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := reopened.AutoAddOf("p1"); !ok {
		t.Fatal("the automatic adding survives a restart")
	}

	if mtime, ok := reopened.HandledMTime("local:q/p2/b.bgcode"); !ok || mtime != 6 {
		t.Fatal("the marks survive a restart")
	}

	if err = reopened.SetAutoAdd("p1", "", false); err != nil {
		t.Fatal(err)
	}

	if len(reopened.AutoDevices()) != 0 {
		t.Fatal("turning the switch off clears it")
	}
}

func TestInboxFolderChoice(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-time.Hour).Unix()

	m := &module{
		inboxStorage: "local",
		inboxFolder:  "print-queue",
		list: func(_ context.Context, _, _, folder string) ([]storageapi.ListEntry, error) {
			switch folder {
			case "":
				return []storageapi.ListEntry{{Name: "COREONEINDX", Dir: true}, {Name: "queue", Dir: true}}, nil
			case "COREONEINDX", "queue":
				return []storageapi.ListEntry{{Name: "a.bgcode", Size: 10, MTime: old}}, nil
			}

			return nil, errors.New("no such folder")
		},
	}

	store, err := OpenStore("", time.Now, 5)
	if err != nil {
		t.Fatal(err)
	}

	device := deviceapi.Device{ID: "p1", Name: "COREONEINDX"}

	view, err := m.inboxScan(context.Background(), "user:operator", device, store, now)
	if err != nil || view.Folder != "COREONEINDX" || view.Custom || len(view.Items) != 1 ||
		view.Items[0].Path != "COREONEINDX/a.bgcode" {
		t.Fatalf("a folder named after the printer at the root is found: %+v %v", view, err)
	}

	if err = store.SetInboxFolder("p1", "queue"); err != nil {
		t.Fatal(err)
	}

	view, err = m.inboxScan(context.Background(), "user:operator", device, store, now)
	if err != nil || view.Folder != "queue" || !view.Custom || len(view.Items) != 1 {
		t.Fatalf("the folder a person chose wins: %+v %v", view, err)
	}

	if err = store.SetInboxFolder("p1", "not/there"); err != nil {
		t.Fatal(err)
	}

	view, err = m.inboxScan(context.Background(), "user:operator", device, store, now)
	if err != nil || view.Folder != "not/there" || len(view.Items) != 0 {
		t.Fatalf("a chosen folder that does not exist yet is an empty inbox: %+v %v", view, err)
	}

	if err = store.SetInboxFolder("p1", ""); err != nil || store.InboxFolderOf("p1") != "" {
		t.Fatal("an empty folder goes back to the automatic one")
	}
}

func TestCleanInboxFolder(t *testing.T) {
	for in, want := range map[string]string{"": "", " /queue/ ": "queue", "a/b": "a/b"} {
		if got, ok := cleanInboxFolder(in); !ok || got != want {
			t.Fatalf("cleanInboxFolder(%q) = %q %v", in, got, ok)
		}
	}

	for _, in := range []string{"../x", "a/../b", ".pcl-meta", "a/.hidden", `a\b`} {
		if _, ok := cleanInboxFolder(in); ok {
			t.Fatalf("cleanInboxFolder(%q) should be refused", in)
		}
	}
}

func TestTheInboxIsListedForThePersonAsking(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-time.Hour).Unix()

	var asked []string

	m := &module{
		inboxStorage: "local",
		inboxFolder:  "print-queue",
		list: func(_ context.Context, principal, _, folder string) ([]storageapi.ListEntry, error) {
			asked = append(asked, principal)

			// Only alice may read the storage.
			if principal != "user:alice" {
				return nil, errors.New("not allowed to read the storage")
			}

			switch folder {
			case "print-queue":
				return []storageapi.ListEntry{{Name: "Core One", Dir: true}}, nil
			case "print-queue/Core One", "secret":
				return []storageapi.ListEntry{{Name: "a.bgcode", Size: 10, MTime: old}}, nil
			}

			return nil, errors.New("no such folder")
		},
	}

	store, err := OpenStore("", time.Now, 5)
	if err != nil {
		t.Fatal(err)
	}

	device := deviceapi.Device{ID: "p1", Name: "Core One"}

	view, err := m.inboxScan(context.Background(), "user:alice", device, store, now)
	if err != nil || len(view.Items) != 1 {
		t.Fatalf("a person who may read the storage sees the files: %+v %v", view, err)
	}

	for _, who := range asked {
		if who != "user:alice" {
			t.Fatalf("the storage was listed for %q, want the person asking", who)
		}
	}

	// A person who may operate the printer but not read the storage sees
	// nothing, whether the folder is found by name or chosen by hand.
	for _, folder := range []string{"", "secret"} {
		if err = store.SetInboxFolder("p1", folder); err != nil {
			t.Fatal(err)
		}

		view, err = m.inboxScan(context.Background(), "user:bob", device, store, now)
		if err != nil || len(view.Items) != 0 {
			t.Fatalf("a person who may not read the storage sees files in %q: %+v %v", folder, view, err)
		}
	}
}

func TestChangingTheFolderTurnsTheAutomaticAddingOff(t *testing.T) {
	store, err := OpenStore("", time.Now, 5)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.SetInboxFolder("p1", "uploads"); err != nil {
		t.Fatal(err)
	}

	if err = store.SetAutoAdd("p1", "user:alice", true); err != nil {
		t.Fatal(err)
	}

	// The same folder again changes nothing.
	if err = store.SetInboxFolder("p1", "uploads"); err != nil {
		t.Fatal(err)
	}

	if who, ok := store.AutoAddOf("p1"); !ok || who != "user:alice" {
		t.Fatalf("the same folder must keep the automatic adding: %q %v", who, ok)
	}

	// Another person aims the printer at another folder: it stops.
	if err = store.SetInboxFolder("p1", "elsewhere"); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.AutoAddOf("p1"); ok {
		t.Fatal("a new folder must turn the automatic adding off")
	}

	// Going back to the automatic folder is a change too.
	if err = store.SetAutoAdd("p1", "user:alice", true); err != nil {
		t.Fatal(err)
	}

	if err = store.SetInboxFolder("p1", ""); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.AutoAddOf("p1"); ok {
		t.Fatal("going back to the automatic folder must turn the automatic adding off")
	}

	// The printers do not affect each other.
	if err = store.SetAutoAdd("p2", "user:bob", true); err != nil {
		t.Fatal(err)
	}

	if err = store.SetInboxFolder("p1", "x"); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.AutoAddOf("p2"); !ok {
		t.Fatal("another printer's automatic adding must stay")
	}
}
