package main

import (
	"context"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"
)

// The inbox is the folder a printer's uploads from PrusaSlicer land in. The
// PrusaSlicer 2 Connector saves each upload to a storage folder the operator
// picks for that printer, and Print Queue reads the folder named after the
// printer: <inbox_folder>/<printer name>.

const (
	// inboxSettle is how long a file stays unchanged before the inbox offers
	// it, so a file still being written is not queued half sent.
	inboxSettle = 8 * time.Second

	// inboxPages bounds the pages read from one folder.
	inboxPages = 5

	// inboxLogEvery spaces the lines the background scan writes for a failure
	// that lasts.
	inboxLogEvery = 10 * time.Minute
)

// inboxItem is one file waiting in a printer's folder.
type inboxItem struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// inboxView is what a printer's folder holds that nobody has handled yet.
type inboxView struct {
	Storage string      `json:"storage"`
	Folder  string      `json:"folder"`
	Auto    bool        `json:"auto"`
	// Custom is true when a person chose the folder, and false when it was
	// found from the printer's name.
	Custom bool        `json:"custom"`
	Items  []inboxItem `json:"items"`
}

// printableFile says whether a file name is one a printer prints.
func printableFile(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".gcode", ".bgcode", ".gco", ".g":
		return true
	}

	return false
}

// folderLabel is the folder name a printer is expected to have: its name with
// the characters a folder refuses replaced, or its id when it has no name.
func folderLabel(device deviceapi.Device) string {
	label := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}

		return r
	}, strings.TrimSpace(device.Name))

	label = strings.Trim(label, ". ")
	if label == "" {
		return device.ID
	}

	return label
}

// sameFolder says whether a folder of the inbox belongs to a printer: it
// carries the printer's id, or its name apart from case and spacing.
func sameFolder(entry, label, id string) bool {
	normal := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

	return entry == id || normal(entry) == normal(label)
}

// listFolder reads every entry of one folder of a storage.
func (m *module) listFolder(ctx context.Context, storage, folder string) ([]storageapi.ListEntry, error) {
	var all []storageapi.ListEntry

	cursor := ""

	for range inboxPages {
		listing, err := m.files.List(ctx, storageapi.ListParams{Storage: storage, Path: folder, Cursor: cursor})
		if err != nil {
			return nil, err
		}

		all = append(all, listing.Entries...)

		if listing.Next == "" {
			break
		}

		cursor = listing.Next
	}

	return all, nil
}

// findInboxFolder looks for the folder named after a printer, first under the
// inbox folder and then at the root of the storage. It answers an empty path
// when there is none.
func (m *module) findInboxFolder(ctx context.Context, device deviceapi.Device) string {
	label := folderLabel(device)

	for _, base := range []string{m.inboxFolder, ""} {
		entries, err := m.list(ctx, m.inboxStorage, base)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.Dir && sameFolder(entry.Name, label, device.ID) {
				return path.Join(base, entry.Name)
			}
		}
	}

	return ""
}

// cleanInboxFolder checks a folder a person typed. An empty answer asks for
// the folder to be found from the printer's name again.
func cleanInboxFolder(folder string) (string, bool) {
	folder = strings.Trim(strings.TrimSpace(folder), "/")
	if folder == "" {
		return "", true
	}

	if !fs.ValidPath(folder) || strings.Contains(folder, "\\") || len(folder) > 512 {
		return "", false
	}

	for _, part := range strings.Split(folder, "/") {
		if strings.HasPrefix(part, ".") {
			return "", false
		}
	}

	return folder, true
}

// inboxScan reads the folder of one printer. A folder that does not exist yet
// is an empty inbox and not a failure.
func (m *module) inboxScan(ctx context.Context, device deviceapi.Device, store *Store, now time.Time) (inboxView, error) {
	view := inboxView{Storage: m.inboxStorage, Items: []inboxItem{}}
	_, view.Auto = store.AutoAddOf(device.ID)

	view.Folder = store.InboxFolderOf(device.ID)
	view.Custom = view.Folder != ""

	if !view.Custom {
		view.Folder = m.findInboxFolder(ctx, device)
	}

	if view.Folder == "" {
		view.Folder = path.Join(m.inboxFolder, folderLabel(device))

		return view, nil
	}

	files, err := m.list(ctx, m.inboxStorage, view.Folder)
	if err != nil {
		if view.Custom {
			return view, nil //nolint:nilerr // the folder may not exist until the first upload
		}

		return view, err
	}

	present := map[string]bool{}

	for _, file := range files {
		if file.Dir || !printableFile(file.Name) {
			continue
		}

		filePath := path.Join(view.Folder, file.Name)
		key := m.inboxStorage + ":" + filePath
		present[key] = true

		if file.Size <= 0 || now.Sub(time.Unix(file.MTime, 0)) < inboxSettle {
			continue
		}

		if handled, ok := store.HandledMTime(key); ok && handled == file.MTime {
			continue
		}

		view.Items = append(view.Items, inboxItem{
			Name: file.Name, Path: filePath, Size: file.Size, Modified: time.Unix(file.MTime, 0).UTC(),
		})
	}

	slices.SortFunc(view.Items, func(a, b inboxItem) int { return b.Modified.Compare(a.Modified) })

	// A file that left the folder no longer needs its mark.
	_ = store.ForgetHandled(m.inboxStorage+":"+view.Folder+"/", present)

	return view, nil
}

// autoAdd puts the new files of every printer that asked for it in its queue,
// oldest first, as the person who turned the switch on.
func (m *module) autoAdd(ctx context.Context) {
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()

	if store == nil || m.list == nil {
		return
	}

	for id, principal := range store.AutoDevices() {
		device, err := m.devices.Get(ctx, deviceapi.GetParams{ID: id})
		if err != nil || device.Retired || (device.Kind != "" && device.Kind != "printer") {
			continue
		}

		view, err := m.inboxScan(ctx, device, store, time.Now())
		if err != nil {
			m.logInbox("read the inbox of %s: %v", id, err)

			continue
		}

		for i := len(view.Items) - 1; i >= 0; i-- {
			item := view.Items[i]
			job := Job{DeviceID: id, Storage: view.Storage, Path: item.Path, Name: item.Name, AddedBy: principal}

			if err = m.authorizeDispatch(ctx, job); err != nil {
				m.logInbox("queue %s for %s: %v", item.Path, id, err)

				break
			}

			if _, err = store.Add(job); err != nil {
				m.logInbox("queue %s for %s: %v", item.Path, id, err)

				break
			}

			if err = store.MarkHandled(view.Storage+":"+item.Path, item.Modified.Unix()); err != nil {
				m.logInbox("remember %s: %v", item.Path, err)

				break
			}
		}
	}
}

// logInbox writes a line about the background scan, at most once in a while.
// Only the loop calls it.
func (m *module) logInbox(format string, args ...any) {
	if time.Since(m.inboxLogged) < inboxLogEvery {
		return
	}

	m.inboxLogged = time.Now()
	logf(format, args...)
}

// inboxDevice answers the store and the printer of an inbox route, or refuses
// the request.
func (m *module) inboxDevice(w http.ResponseWriter, r *http.Request) (*Store, deviceapi.Device, bool) {
	store := m.ready(w, r)
	if store == nil {
		return nil, deviceapi.Device{}, false
	}

	id := r.PathValue("id")
	if !m.requireOperate(w, r, id) {
		return nil, deviceapi.Device{}, false
	}

	device, err := m.devices.Get(r.Context(), deviceapi.GetParams{Principal: routesapi.PrincipalOf(r), ID: id})
	if err != nil || device.Retired || (device.Kind != "" && device.Kind != "printer") {
		refuse(w, r, NoDevice.New("the printer is unavailable"))

		return nil, deviceapi.Device{}, false
	}

	return store, device, true
}

func (m *module) registerInbox(mux *http.ServeMux) {
	mux.HandleFunc("GET /devices/{id}/inbox", func(w http.ResponseWriter, r *http.Request) {
		store, device, ok := m.inboxDevice(w, r)
		if !ok {
			return
		}

		view, err := m.inboxScan(r.Context(), device, store, time.Now())
		if err != nil {
			logf("read the inbox of %s: %v", device.ID, err)
			refuse(w, r, StorageUnavailable.New("the storage could not be read"))

			return
		}

		answer(w, http.StatusOK, view)
	})

	mux.HandleFunc("POST /devices/{id}/inbox/dismiss", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}

		if !decode(w, r, &body) {
			return
		}

		store, device, ok := m.inboxDevice(w, r)
		if !ok {
			return
		}

		view, err := m.inboxScan(r.Context(), device, store, time.Now())
		if err != nil {
			logf("read the inbox of %s: %v", device.ID, err)
			refuse(w, r, StorageUnavailable.New("the storage could not be read"))

			return
		}

		for _, item := range view.Items {
			if item.Path == body.Path {
				m.writeResult(w, r, store.MarkHandled(view.Storage+":"+item.Path, item.Modified.Unix()))

				return
			}
		}

		// Already handled, or gone.
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("PUT /devices/{id}/inbox-folder", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Folder string `json:"folder"`
		}

		if !decode(w, r, &body) {
			return
		}

		folder, valid := cleanInboxFolder(body.Folder)
		if !valid {
			refuse(w, r, BadRequest.New("folder must be a folder of the storage"))

			return
		}

		store, device, ok := m.inboxDevice(w, r)
		if !ok {
			return
		}

		m.writeResult(w, r, store.SetInboxFolder(device.ID, folder))
	})

	mux.HandleFunc("PUT /devices/{id}/inbox-auto", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}

		if !decode(w, r, &body) {
			return
		}

		store, device, ok := m.inboxDevice(w, r)
		if !ok {
			return
		}

		m.writeResult(w, r, store.SetAutoAdd(device.ID, routesapi.PrincipalOf(r), body.Enabled))
	})
}

// AutoAddOf says who turned the automatic adding on for a printer.
func (s *Store) AutoAddOf(deviceID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	who, ok := s.data.AutoAdd[deviceID]

	return who, ok
}

// AutoDevices answers every printer with the automatic adding on, and who
// turned it on.
func (s *Store) AutoDevices() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	devices := make(map[string]string, len(s.data.AutoAdd))
	for id, who := range s.data.AutoAdd {
		devices[id] = who
	}

	return devices
}

// SetAutoAdd turns the automatic adding of a printer's uploads on or off.
func (s *Store) SetAutoAdd(deviceID, principal string, on bool) error {
	return s.update(func(data *snapshot) error {
		if !on {
			delete(data.AutoAdd, deviceID)

			return nil
		}

		if data.AutoAdd == nil {
			data.AutoAdd = map[string]string{}
		}

		data.AutoAdd[deviceID] = principal

		return nil
	})
}

// HandledMTime answers the modification time a file had when it was added to a
// queue or ignored.
func (s *Store) HandledMTime(key string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mtime, ok := s.data.Handled[key]

	return mtime, ok
}

// MarkHandled remembers that a file, as it was last changed, was added or
// ignored. A file saved again under the same name is offered again.
func (s *Store) MarkHandled(key string, mtime int64) error {
	return s.update(func(data *snapshot) error {
		if data.Handled == nil {
			data.Handled = map[string]int64{}
		}

		data.Handled[key] = mtime

		return nil
	})
}

// ForgetHandled drops the marks under prefix whose file is no longer present.
func (s *Store) ForgetHandled(prefix string, keep map[string]bool) error {
	return s.update(func(data *snapshot) error {
		for key := range data.Handled {
			if strings.HasPrefix(key, prefix) && !keep[key] {
				delete(data.Handled, key)
			}
		}

		return nil
	})
}

// InboxFolderOf answers the folder a person chose for a printer's uploads, or
// an empty path when none was chosen.
func (s *Store) InboxFolderOf(deviceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.data.InboxFolders[deviceID]
}

// SetInboxFolder chooses the folder of a printer's uploads. An empty folder
// goes back to the folder named after the printer.
func (s *Store) SetInboxFolder(deviceID, folder string) error {
	return s.update(func(data *snapshot) error {
		if folder == "" {
			delete(data.InboxFolders, deviceID)

			return nil
		}

		if data.InboxFolders == nil {
			data.InboxFolders = map[string]string{}
		}

		data.InboxFolders[deviceID] = folder

		return nil
	})
}
