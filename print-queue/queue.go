// The queue's data and the scheduler that moves each job through it.
//
// A job is added as `queued`, becomes `starting` when the scheduler sends it to
// the printer, `printing` once the printer reports the print, and ends as
// `done`, `stopped` or `failed`. Store keeps every job on disk. Scheduler reads
// the printers and changes the jobs.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// The states a job moves through. A job is added as queued, becomes starting
// when the module sends it to the printer, printing once the printer reports a
// print, and ends as done, stopped or failed.
const (
	statusQueued   = "queued"
	statusStarting = "starting"
	statusPrinting = "printing"
	statusDone     = "done"
	statusStopped  = "stopped"
	statusFailed   = "failed"
)

// The printer states this module reads, as the Buddy firmware names them.
const (
	printerReady = "READY"
)

var (
	errNoJob      = errors.New("no such job")
	errJobRunning = errors.New("the job is being printed")
)

// Job is one entry of a printer's queue.
type Job struct {
	ID       string `json:"id"`
	DeviceID string `json:"device_id"`
	// Storage names the server storage the file is read from. It is empty for
	// a file the printer already holds on its own drive, which is then printed
	// where it is and sent nowhere.
	Storage string `json:"storage"`
	// Path is the file's path inside Storage, or on the printer when Storage
	// is empty.
	Path string `json:"path"`
	Name string `json:"name"`
	// AddedBy is the person whose rights are checked again when the job
	// starts, so a person who lost access cannot print through the queue.
	AddedBy string    `json:"added_by,omitempty"`
	AddedAt time.Time `json:"added_at"`
	// NotBefore holds the job until that time. Nil means as soon as possible.
	NotBefore *time.Time `json:"not_before,omitempty"`
	Status    string     `json:"status"`
	// Detail is the sentence for DetailCode, kept so older snapshots read well.
	// DetailCode says why a job failed or waits, and is a key of jobDetails.
	Detail     string `json:"detail,omitempty"`
	DetailCode string `json:"detail_code,omitempty"`
	// PrinterPath is where the file sits on the printer once sent. A file from
	// storage gets a unique name there, so it never overwrites another file.
	PrinterPath string     `json:"printer_path,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	// TransferID, TransferDone and CommandID follow the send and the start
	// command through the host's events. PrintID is the print the printer
	// reports, which ties the job to the host's job history.
	TransferID   string `json:"transfer_id,omitempty"`
	TransferDone bool   `json:"transfer_done,omitempty"`
	CommandID    string `json:"command_id,omitempty"`
	PrintID      string `json:"print_id,omitempty"`

	// The copy made ahead of time. While the job waits for the printer to be
	// ready, the file of the next job is already copied onto the printer, so
	// the print starts at once. StagedPath is where the copy goes on the
	// printer. StageTransferID names the transfer until it settles, and Staged
	// says the copy has landed. StagedAt is when the copy was asked for.
	StagedPath      string     `json:"staged_path,omitempty"`
	StageTransferID string     `json:"stage_transfer_id,omitempty"`
	Staged          bool       `json:"staged,omitempty"`
	StagedAt        *time.Time `json:"staged_at,omitempty"`
}

var jobDetails = map[string]string{
	"unconfirmed":       "The result is uncertain. Check the printer before resuming the queue.",
	"permissionChanged": "Could not verify the permissions of the person who queued this job.",
	"missingFile":       "The file is no longer in storage.",
	"printerPermission": "The extension cannot start a print on this printer.",
	"startRefused":      "The printer refused to start the print.",
	"startFailed":       "The printer could not start the print.",
	"nameTaken":         "A file with the same name already exists on the printer.",
	"dispatchFailed":    "Prusa Connect did not accept the job.",
	"transferFailed":    "The transfer to the printer failed.",
	"startRejected":     "The printer refused or could not start the print.",
	"printFailed":       "The print failed.",
}

// Older snapshots may only have the original French text, with no detail code.
var legacyJobDetails = map[string]string{
	"unconfirmed":       "Le résultat est incertain. Vérifie l’imprimante avant de reprendre la file.",
	"permissionChanged": "Les droits de la personne qui a mis ce travail en file n’ont pas pu être confirmés.",
	"missingFile":       "Le fichier n'existe plus dans le stockage.",
	"printerPermission": "Le module n'a pas le droit de lancer une impression sur cette imprimante.",
	"startRefused":      "L’imprimante a refusé de démarrer.",
	"startFailed":       "L’imprimante n’a pas pu démarrer.",
	"nameTaken":         "Un fichier du même nom existe déjà sur l'imprimante.",
	"dispatchFailed":    "Prusa Connect n’a pas accepté le travail.",
	"transferFailed":    "Le transfert vers l’imprimante a échoué.",
	"startRejected":     "L’imprimante a refusé ou n’a pas pu démarrer l’impression.",
	"printFailed":       "L’impression a échoué.",
}

func (j *Job) setDetail(code string) {
	j.DetailCode = code
	j.Detail = jobDetails[code]
}

// stageTimeout is how long a copy ahead of time may stay unsettled before the
// job forgets it and the file is sent again when the print starts.
const stageTimeout = 10 * time.Minute

// stageRetryAfter is how long the scheduler leaves a job alone after a copy
// ahead of time failed, for example because the printer was busy receiving
// another file.
const stageRetryAfter = time.Minute

// maxStaged bounds the files copied ahead of time for one printer, so a queue
// that is reordered does not fill the printer's drive.
const maxStaged = 2

// resetStaging forgets the copy ahead of time. The file may still be on the
// printer, but the job no longer relies on it.
func (j *Job) resetStaging() {
	j.StagedPath = ""
	j.StageTransferID = ""
	j.Staged = false
	j.StagedAt = nil
}

// finished says whether the job has left the queue for good.
func (j Job) finished() bool {
	return j.Status == statusDone || j.Status == statusStopped || j.Status == statusFailed
}

// snapshot is everything the module keeps, written whole to one file.
type snapshot struct {
	Jobs []Job `json:"jobs"`
	// Paused holds the queue of a printer: nothing starts until it is lifted.
	Paused map[string]bool `json:"paused"`
	// Cursor is the last event of the host's stream this module has handled,
	// so a restart resumes where it stopped (see print_events.go).
	Cursor string `json:"cursor,omitempty"`
	// UnconfirmedReady holds the printers marked ready by hand, until the
	// printer itself reports READY (see ready.go).
	UnconfirmedReady map[string]readyMarker `json:"plate_ready,omitempty"`
	// AutoAdd names, for each printer whose uploads from PrusaSlicer join its
	// queue by themselves, the person who asked for it.
	AutoAdd map[string]string `json:"auto_add,omitempty"`
	// Handled is the modification time of each inbox file that was added to a
	// queue or ignored, by storage and path.
	Handled map[string]int64 `json:"inbox_handled,omitempty"`
	// InboxFolders is the folder a person chose for the uploads of a printer.
	InboxFolders map[string]string `json:"inbox_folders,omitempty"`
}

// nextQueued answers the first job of a printer that is waiting and whose
// start time has come, in queue order. A job held until later is skipped, so it
// does not block the ones behind it.
func (data *snapshot) nextQueued(deviceID string, now time.Time) *Job {
	for i := range data.Jobs {
		job := &data.Jobs[i]
		if job.DeviceID == deviceID && job.Status == statusQueued &&
			(job.NotBefore == nil || !job.NotBefore.After(now)) {
			return job
		}
	}
	return nil
}

// Store holds the queues in memory and writes them to queue.json in the data
// directory after every change. Without a data directory it keeps them in
// memory only, and a restart forgets them.
type Store struct {
	mu   sync.Mutex
	file string
	data snapshot
	now  func() time.Time
	keep int
}

// OpenStore reads queue.json from dir, or starts empty.
func OpenStore(dir string, now func() time.Time, keep int) (*Store, error) {
	s := &Store{now: now, keep: keep, data: snapshot{Paused: map[string]bool{}}}
	if dir == "" {
		return s, nil
	}

	s.file = filepath.Join(dir, "queue.json")

	raw, err := os.ReadFile(s.file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", s.file, err)
	}

	if err = json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("read %s: %w", s.file, err)
	}

	for i := range s.data.Jobs {
		job := &s.data.Jobs[i]
		if job.Detail != "" && job.DetailCode == "" {
			for code, message := range legacyJobDetails {
				if job.Detail == message {
					job.DetailCode = code
					break
				}
			}
		}
		if message, ok := jobDetails[job.DetailCode]; ok {
			job.Detail = message
		}
	}

	if s.data.Paused == nil {
		s.data.Paused = map[string]bool{}
	}

	return s, nil
}

// save writes the snapshot under a temporary name and moves it in place, so a
// crash leaves the old file or the new one and never half of one. It is called
// with the lock held.
func (s *Store) save(data snapshot) error {
	if s.file == "" {
		return nil
	}

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.file + ".tmp"
	if err = os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, s.file)
}

// update runs change on the snapshot under the lock and saves the result.
func (s *Store) update(change func(*snapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := snapshot{
		Jobs: slices.Clone(s.data.Jobs), Paused: maps.Clone(s.data.Paused),
		Cursor: s.data.Cursor, UnconfirmedReady: maps.Clone(s.data.UnconfirmedReady),
		AutoAdd: maps.Clone(s.data.AutoAdd), Handled: maps.Clone(s.data.Handled),
		InboxFolders: maps.Clone(s.data.InboxFolders),
	}
	if err := change(&next); err != nil {
		return err
	}
	if reflect.DeepEqual(next, s.data) {
		return nil
	}

	next.Jobs = trim(next.Jobs, s.keep)
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

// trim keeps the newest finished jobs of each printer and drops the rest.
func trim(jobs []Job, keep int) []Job {

	seen := map[string]int{}
	kept := make([]Job, 0, len(jobs))

	for i := len(jobs) - 1; i >= 0; i-- {
		job := jobs[i]
		if job.finished() {
			seen[job.DeviceID]++
			if seen[job.DeviceID] > keep {
				continue
			}
		}

		kept = append(kept, job)
	}

	slices.Reverse(kept)
	return kept
}

// View is what the routes answer for one printer, or for all of them.
type View struct {
	Jobs                      []Job           `json:"jobs"`
	Paused                    map[string]bool `json:"paused"`
	ReadyConfirmationRequired map[string]bool `json:"ready_confirmation_required,omitempty"`
}

// List answers the jobs of one printer in queue order, or of every printer
// when device is empty.
func (s *Store) List(device string) View {
	s.mu.Lock()
	defer s.mu.Unlock()

	view := View{Jobs: []Job{}, Paused: map[string]bool{}}

	for _, job := range s.data.Jobs {
		if device == "" || job.DeviceID == device {
			view.Jobs = append(view.Jobs, job)
		}
	}

	for id, paused := range s.data.Paused {
		if paused && (device == "" || id == device) {
			view.Paused[id] = true
		}
	}
	for id := range s.data.UnconfirmedReady {
		if device == "" || id == device {
			if view.ReadyConfirmationRequired == nil {
				view.ReadyConfirmationRequired = make(map[string]bool)
			}
			view.ReadyConfirmationRequired[id] = true
		}
	}

	return view
}

// deviceIDs lists, in order, every printer the view says something about.
func (v View) deviceIDs() []string {
	ids := make(map[string]struct{}, len(v.Paused))
	for id := range v.Paused {
		ids[id] = struct{}{}
	}
	for id := range v.ReadyConfirmationRequired {
		ids[id] = struct{}{}
	}
	for _, job := range v.Jobs {
		ids[job.DeviceID] = struct{}{}
	}
	return slices.Sorted(maps.Keys(ids))
}

// job answers a copy of one job, and false when the queue holds no such job.
func (s *Store) job(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := indexOf(s.data.Jobs, id)
	if i < 0 {
		return Job{}, false
	}
	return s.data.Jobs[i], true
}

// Add puts a job at the end of its printer's queue.
func (s *Store) Add(job Job) (Job, error) {
	job.ID = newID()
	job.AddedAt = s.now().UTC()
	job.Status = statusQueued

	if job.Name == "" {
		job.Name = path.Base(job.Path)
	}

	err := s.update(func(data *snapshot) error {
		data.Jobs = append(data.Jobs, job)

		return nil
	})

	return job, err
}

// Remove takes a job out. A job being sent or printed stays: stop the print
// on the printer instead.
func (s *Store) Remove(id string) error {
	return s.update(func(data *snapshot) error {
		i := indexOf(data.Jobs, id)
		if i < 0 {
			return errNoJob
		}

		if data.Jobs[i].Status == statusStarting || data.Jobs[i].Status == statusPrinting {
			return errJobRunning
		}

		data.Jobs = slices.Delete(data.Jobs, i, i+1)

		return nil
	})
}

// Move swaps a queued job with the queued job of the same printer before it
// (up) or after it (down). A job at the end of its queue stays.
func (s *Store) Move(id string, up bool) error {
	return s.update(func(data *snapshot) error {
		i := indexOf(data.Jobs, id)
		if i < 0 {
			return errNoJob
		}

		if data.Jobs[i].Status != statusQueued {
			return errJobRunning
		}

		step := 1
		if up {
			step = -1
		}

		for j := i + step; j >= 0 && j < len(data.Jobs); j += step {
			other := data.Jobs[j]
			if other.DeviceID == data.Jobs[i].DeviceID && other.Status == statusQueued {
				data.Jobs[i], data.Jobs[j] = data.Jobs[j], data.Jobs[i]

				return nil
			}
		}

		return nil
	})
}

// Schedule sets the time a job may start at, or clears it with nil.
func (s *Store) Schedule(id string, at *time.Time) error {
	return s.update(func(data *snapshot) error {
		i := indexOf(data.Jobs, id)
		if i < 0 {
			return errNoJob
		}

		if data.Jobs[i].Status != statusQueued {
			return errJobRunning
		}

		if at != nil {
			utc := at.UTC()
			at = &utc
		}

		data.Jobs[i].NotBefore = at

		return nil
	})
}

// Requeue puts a failed or ended job back at the end of the queue.
func (s *Store) Requeue(id, principal string) error {
	return s.update(func(data *snapshot) error {
		i := indexOf(data.Jobs, id)
		if i < 0 {
			return errNoJob
		}

		job := data.Jobs[i]
		if !job.finished() {
			return errJobRunning
		}

		job.Status = statusQueued
		job.setDetail("")
		job.PrinterPath = ""
		job.TransferID = ""
		job.TransferDone = false
		job.CommandID = ""
		job.PrintID = ""
		job.resetStaging()
		job.AddedBy = principal
		job.StartedAt = nil
		job.EndedAt = nil
		job.NotBefore = nil

		data.Jobs = append(slices.Delete(data.Jobs, i, i+1), job)

		return nil
	})
}

// SetPaused holds a printer's queue, or lets it run again.
func (s *Store) SetPaused(device string, paused bool) error {
	return s.update(func(data *snapshot) error {
		if paused {
			data.Paused[device] = true
		} else {
			delete(data.Paused, device)
		}

		return nil
	})
}

// ClearFinished drops the finished jobs of one printer, or of every printer.
func (s *Store) ClearFinished(devices map[string]bool) error {
	return s.update(func(data *snapshot) error {
		data.Jobs = slices.DeleteFunc(data.Jobs, func(job Job) bool {
			return job.finished() && devices[job.DeviceID]
		})

		return nil
	})
}

// indexOf answers the position of a job in the list, or -1.
func indexOf(jobs []Job, id string) int {
	return slices.IndexFunc(jobs, func(job Job) bool { return job.ID == id })
}

// newID answers a random identifier for a job.
func newID() string {
	var raw [8]byte

	_, _ = rand.Read(raw[:])

	return hex.EncodeToString(raw[:])
}

// fleet is the part of the host's device client the scheduler uses, so a test
// can stand in for it.
type fleet interface {
	Get(ctx context.Context, params deviceapi.GetParams) (deviceapi.Device, error)
	Print(ctx context.Context, params deviceapi.PrintParams) (deviceapi.Started, error)
	Send(ctx context.Context, params deviceapi.SendParams) (deviceapi.Sent, error)
	Jobs(ctx context.Context, params deviceapi.JobsParams) (deviceapi.Jobs, error)
	JobHistory(ctx context.Context, params deviceapi.JobHistoryParams) (deviceapi.JobPage, error)
}

// Scheduler starts the next job of each printer when the printer says it is
// ready, and follows the job until the printer reports its end.
type Scheduler struct {
	store *Store
	fleet fleet
	now   func() time.Time
	// startTimeout is how long a job may stay `starting` before it is held as
	// unconfirmed.
	startTimeout time.Duration
	// folder is the printer folder a file from storage is sent to.
	folder string
	logf   func(format string, args ...any)
	// authorize checks again, at the moment of the start, that the person who
	// queued a job may still print on the printer and read the file.
	authorize func(context.Context, Job) error
	command   func(context.Context, deviceapi.CommandParams) (deviceapi.Result, error)
	mu        sync.Mutex // orders dispatch and stream windows
	// following is true once the host's event stream is being read. No job
	// starts before that, so a print is never started blind.
	following bool
	// prestage turns on the copy of the next file onto the printer ahead of
	// time. stageRetry holds, by job, when a failed copy may be tried again.
	prestage   bool
	stageRetry map[string]time.Time
}

// printerState reads the printer's own word for what it is doing.
func printerState(device deviceapi.Device) string {
	raw, ok := device.State["/state"]
	if !ok {
		return ""
	}

	var state string
	if err := json.Unmarshal(raw, &state); err != nil {
		return ""
	}

	return strings.ToUpper(state)
}

// Tick looks at every printer that has a job waiting or running, once.
func (sc *Scheduler) Tick(ctx context.Context) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, device := range sc.devicesWithWork() {
		if ctx.Err() != nil {
			return
		}

		sc.tickDevice(ctx, device)
	}
}

// devicesWithWork lists the printers that have a job to start or follow, or a
// manual ready mark to settle, so Tick skips the idle ones.
func (sc *Scheduler) devicesWithWork() []string {
	sc.store.mu.Lock()
	defer sc.store.mu.Unlock()

	var devices []string

	for _, job := range sc.store.data.Jobs {
		if (!job.finished() || job.DetailCode == "unconfirmed") && !slices.Contains(devices, job.DeviceID) {
			devices = append(devices, job.DeviceID)
		}
	}
	for id := range sc.store.data.UnconfirmedReady {
		if !slices.Contains(devices, id) {
			devices = append(devices, id)
		}
	}

	return devices
}

// tickDevice looks at one printer once, in these steps:
//
//  1. Reconcile the queue with the printer's job data (syncPrints).
//  2. Under one save of the queue: hold any job that lost track of its print
//     as unconfirmed, settle the manual ready mark, and pick the next job.
//  3. Send the picked job, after the save, so a crash cannot lose the attempt.
//
// The next job starts only when the printer is connected and reports READY,
// nothing else is starting or printing on it, its queue is not held, and no
// ready mark is waiting for the printer to confirm.
func (sc *Scheduler) tickDevice(ctx context.Context, deviceID string) {
	now := sc.now().UTC()
	sc.syncPrints(ctx, deviceID, now)
	if !sc.following || ctx.Err() != nil {
		return
	}

	device, err := sc.fleet.Get(ctx, deviceapi.GetParams{ID: deviceID})
	if err != nil {
		sc.logf("read printer %s: %v", deviceID, err)
	}

	state := printerState(device)
	now = sc.now().UTC()
	available := err == nil && device.Connected && !device.Retired
	var next *Job
	guarded := false

	busy := false

	err = sc.store.update(func(data *snapshot) error {
		for i := range data.Jobs {
			job := &data.Jobs[i]
			if job.DeviceID != deviceID {
				continue
			}

			switch job.Status {
			case statusQueued:
				// A copy that never settled is forgotten: the file is then
				// sent as usual when the print starts.
				if job.StagedPath != "" && !job.Staged && job.StagedAt != nil && now.Sub(*job.StagedAt) > stageTimeout {
					job.resetStaging()
				}
			case statusStarting:
				busy = true
				if job.TransferID == "" && job.CommandID == "" {
					holdUnconfirmed(data, job, now)
				} else if job.StartedAt != nil && now.Sub(*job.StartedAt) > sc.startTimeout {
					holdUnconfirmed(data, job, now)
				}
			case statusPrinting:
				busy = true
				if job.PrintID == "" {
					holdUnconfirmed(data, job, now)
				}
			}
		}

		// A printer marked ready by hand stays guarded until it has itself
		// reported READY and then left it, so a stale mark never releases a job.
		marker, exists := data.UnconfirmedReady[deviceID]
		guarded = exists
		if available && exists {
			switch state {
			case printerReady:
				marker.Seen = true
				data.UnconfirmedReady[deviceID] = marker
			case "IDLE", "FINISHED", "PRINTING", "STOPPED":
				if marker.Seen {
					delete(data.UnconfirmedReady, deviceID)
					guarded = false
				}
			}
		}
		if !available || busy || data.Paused[deviceID] {
			return nil
		}
		job := data.nextQueued(deviceID, now)
		if job == nil {
			return nil
		}
		if state != printerReady || guarded {
			return nil
		}
		// A copy ahead of time is still travelling: wait for it, so the file is
		// not sent twice.
		if job.StageTransferID != "" && !job.Staged {
			return nil
		}
		// Save the attempt before any printer call, including the unique
		// upload destination if the file lives in server storage.
		job.Status = statusStarting
		job.StartedAt = &now
		job.setDetail("")
		job.PrinterPath = job.Path
		if job.Storage != "" {
			if job.Staged && job.StagedPath != "" {
				// The file is already on the printer: print that copy.
				job.PrinterPath = job.StagedPath
			} else {
				job.PrinterPath = path.Join(sc.folder, newID()+"-"+path.Base(job.Path))
			}
		}
		copied := *job
		next = &copied
		return nil
	})
	if err != nil {
		sc.logf("save the queue: %v", err)

		return
	}

	if next != nil {
		sc.start(ctx, *next)

		return
	}

	// Nothing starts now: use the wait to copy the next file onto the printer.
	if available {
		sc.stageNext(ctx, deviceID, now)
	}
}

// stageNext copies the file of the job that will start next onto the printer,
// without starting it, while the printer is not ready yet. When the printer
// reports READY, the print then starts at once and the transfer is already
// done.
//
// It copies at most one file at a time for a printer, never while a job is
// being sent or held, and only a file from storage (a file already on the
// printer needs no copy). The attempt is saved before the printer is asked, as
// for a start, and a failure leaves the job as it was: the file is then sent
// when the print starts, as before.
func (sc *Scheduler) stageNext(ctx context.Context, deviceID string, now time.Time) {
	if !sc.prestage {
		return
	}

	if sc.stageRetry == nil {
		sc.stageRetry = map[string]time.Time{}
	}

	var picked *Job

	err := sc.store.update(func(data *snapshot) error {
		if data.Paused[deviceID] {
			return nil
		}

		staged := 0

		for i := range data.Jobs {
			job := &data.Jobs[i]
			if job.DeviceID != deviceID {
				continue
			}

			if job.Status == statusStarting {
				return nil
			}

			if job.Status == statusQueued && job.StagedPath != "" {
				staged++
			}
		}

		next := data.nextQueued(deviceID, now)
		if next == nil || next.Storage == "" || next.StagedPath != "" || staged >= maxStaged {
			return nil
		}

		if retry, ok := sc.stageRetry[next.ID]; ok && now.Before(retry) {
			return nil
		}

		// One copy at a time: a copy still travelling for another job of this
		// printer would make the printer refuse this one.
		for i := range data.Jobs {
			job := &data.Jobs[i]
			if job.DeviceID == deviceID && job.StageTransferID != "" && !job.Staged {
				return nil
			}
		}

		next.StagedPath = path.Join(sc.folder, newID()+"-"+path.Base(next.Path))
		next.Staged = false
		next.StageTransferID = ""
		next.StagedAt = &now
		copied := *next
		picked = &copied

		return nil
	})
	if err != nil {
		sc.logf("save the queue: %v", err)

		return
	}

	if picked == nil {
		return
	}

	denied := sc.authorize(ctx, *picked)

	var (
		sent    deviceapi.Sent
		sendErr error
	)

	if denied == nil {
		sent, sendErr = sc.fleet.Send(ctx, deviceapi.SendParams{
			ID: picked.DeviceID, Path: picked.StagedPath, Storage: picked.Storage, Source: picked.Path,
		})
	}

	failed := denied != nil || sendErr != nil
	if failed {
		wait := stageRetryAfter
		if denied != nil {
			// The person may have lost the right. The start will say so.
			wait = 10 * stageRetryAfter
		}

		sc.stageRetry[picked.ID] = now.Add(wait)
		sc.logf("copy %s to printer %s ahead of time: %v", picked.Path, picked.DeviceID, errors.Join(denied, sendErr))
	}

	if err = sc.store.update(func(data *snapshot) error {
		i := indexOf(data.Jobs, picked.ID)
		if i < 0 {
			return errNoJob
		}

		current := &data.Jobs[i]
		// The job started, was requeued or was changed while the printer was
		// asked: leave it as it is.
		if current.Status != statusQueued || current.StagedPath != picked.StagedPath {
			return nil
		}

		switch {
		case failed:
			current.resetStaging()
		case sent.TransferID == "":
			// The printer already held this file.
			current.Staged = true
		default:
			current.StageTransferID = sent.TransferID
		}

		if !failed && sent.Path != "" {
			current.StagedPath = sent.Path
		}

		return nil
	}); err != nil && !errors.Is(err, errNoJob) {
		sc.logf("save the copy of job %s: %v", picked.ID, err)
	}
}

// holdUnconfirmed requires an operator to check the printer before resuming.
func holdUnconfirmed(data *snapshot, job *Job, now time.Time) {
	job.Status = statusFailed
	job.setDetail("unconfirmed")
	job.EndedAt = &now
	data.Paused[job.DeviceID] = true
}

// start dispatches a job only after checking the requesting user's current rights.
//
// The job is already saved as `starting`. A refusal to answer (cancelled or
// too many questions) puts it back in the queue, a lost permission fails it, and
// a print the printer may already be reading is held as unconfirmed rather than
// risk starting it twice. Success only records the transfer and command ids:
// the events then carry the job to `printing` and to its end.
func (sc *Scheduler) start(ctx context.Context, job Job) {
	params := deviceapi.PrintParams{ID: job.DeviceID, Path: job.PrinterPath}
	// A file copied ahead of time is already on the printer: it is printed
	// where it is, and nothing is sent.
	if job.Storage != "" && !job.Staged {
		params.Storage = job.Storage
		params.Source = job.Path
	}

	denied := sc.authorize(ctx, job)
	var started deviceapi.Started
	var printErr error
	if denied == nil {
		started, printErr = sc.fleet.Print(ctx, params)
	}

	if err := sc.store.update(func(data *snapshot) error {
		i := indexOf(data.Jobs, job.ID)
		if i < 0 {
			return errNoJob
		}
		current := &data.Jobs[i]
		if denied != nil {
			// A transient refusal to answer should not discard the job.
			if errors.Is(denied, context.Canceled) || errors.Is(denied, context.DeadlineExceeded) ||
				errors.Is(denied, accessapi.ErrTooManyQuestions) {
				current.Status = statusQueued
				current.StartedAt = nil
				current.PrinterPath = ""
				return nil
			}
			now := sc.now().UTC()
			current.Status = statusFailed
			current.setDetail("permissionChanged")
			current.EndedAt = &now
			return nil
		}
		if printErr != nil {
			now := sc.now().UTC()
			if job.Staged && errors.Is(printErr, deviceapi.ErrNoFile) {
				// The copy ahead of time is gone from the printer. Send the file
				// again, as for a job that was never copied.
				current.resetStaging()
				current.Status = statusQueued
				current.StartedAt = nil
				current.PrinterPath = ""

				return nil
			}
			if errors.Is(printErr, deviceapi.ErrPrintFailed) || errors.Is(printErr, context.Canceled) ||
				errors.Is(printErr, context.DeadlineExceeded) {
				holdUnconfirmed(data, current, now)
				return nil
			}

			if errors.Is(printErr, deviceapi.ErrDenied) {
				data.Paused[current.DeviceID] = true
			}
			current.Status = statusFailed
			current.setDetail(printErrorCode(printErr))
			current.EndedAt = &now
			return nil
		}
		current.TransferID = started.TransferID
		current.CommandID = started.CommandID
		return nil
	}); err != nil {
		sc.logf("save dispatch of job %s: %v", job.ID, err)
	}
}

// printErrorCode selects the detail for a refused or failed launch.
func printErrorCode(err error) string {
	switch {
	case errors.Is(err, deviceapi.ErrNoFile):
		return "missingFile"
	case errors.Is(err, deviceapi.ErrDenied):
		return "printerPermission"
	case errors.Is(err, deviceapi.ErrPrintRefused):
		return "startRefused"
	case errors.Is(err, deviceapi.ErrPrintFailed):
		return "startFailed"
	case errors.Is(err, deviceapi.ErrNameTaken):
		return "nameTaken"
	}

	return "dispatchFailed"
}
