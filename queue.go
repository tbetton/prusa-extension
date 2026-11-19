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
	ID           string     `json:"id"`
	DeviceID     string     `json:"device_id"`
	Storage      string     `json:"storage"`
	Path         string     `json:"path"`
	Name         string     `json:"name"`
	AddedBy      string     `json:"added_by,omitempty"`
	AddedAt      time.Time  `json:"added_at"`
	NotBefore    *time.Time `json:"not_before,omitempty"`
	Status       string     `json:"status"`
	Detail       string     `json:"detail,omitempty"`
	DetailCode   string     `json:"detail_code,omitempty"`
	PrinterPath  string     `json:"printer_path,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	TransferID   string     `json:"transfer_id,omitempty"`
	TransferDone bool       `json:"transfer_done,omitempty"`
	CommandID    string     `json:"command_id,omitempty"`
	PrintID      string     `json:"print_id,omitempty"`
}

// The French text keeps the detail field readable for API clients and lets
// older snapshots recover the stable code used by the browser.
var jobDetails = map[string]string{
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

// finished says whether the job has left the queue for good.
func (j Job) finished() bool {
	return j.Status == statusDone || j.Status == statusStopped || j.Status == statusFailed
}

// snapshot is everything the module keeps, written whole to one file.
type snapshot struct {
	Jobs   []Job           `json:"jobs"`
	Paused map[string]bool `json:"paused"`
	Cursor string          `json:"cursor,omitempty"`
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
			for code, message := range jobDetails {
				if job.Detail == message {
					job.DetailCode = code
					break
				}
			}
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

	next := snapshot{Jobs: slices.Clone(s.data.Jobs), Paused: maps.Clone(s.data.Paused), Cursor: s.data.Cursor}
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
	Jobs   []Job           `json:"jobs"`
	Paused map[string]bool `json:"paused"`
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

	return view
}

func (v View) deviceIDs() []string {
	ids := make(map[string]struct{}, len(v.Paused))
	for id := range v.Paused {
		ids[id] = struct{}{}
	}
	for _, job := range v.Jobs {
		ids[job.DeviceID] = struct{}{}
	}
	return slices.Sorted(maps.Keys(ids))
}

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

func indexOf(jobs []Job, id string) int {
	return slices.IndexFunc(jobs, func(job Job) bool { return job.ID == id })
}

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
}

// Scheduler starts the next job of each printer when the printer says it is
// ready, and follows the job until the printer reports its end.
type Scheduler struct {
	store        *Store
	fleet        fleet
	now          func() time.Time
	startTimeout time.Duration
	folder       string
	logf         func(format string, args ...any)
	authorize    func(context.Context, Job) error
	mu           sync.Mutex // orders dispatch and stream windows
	following    bool
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
	if !sc.following {
		return
	}
	for _, device := range sc.devicesWithWork() {
		if ctx.Err() != nil {
			return
		}

		sc.tickDevice(ctx, device)
	}
}

func (sc *Scheduler) devicesWithWork() []string {
	sc.store.mu.Lock()
	defer sc.store.mu.Unlock()

	var devices []string

	for _, job := range sc.store.data.Jobs {
		if !job.finished() && !slices.Contains(devices, job.DeviceID) {
			devices = append(devices, job.DeviceID)
		}
	}

	return devices
}

func (sc *Scheduler) tickDevice(ctx context.Context, deviceID string) {
	device, err := sc.fleet.Get(ctx, deviceapi.GetParams{ID: deviceID})
	if err != nil {
		sc.logf("read printer %s: %v", deviceID, err)
	}

	state := printerState(device)
	now := sc.now().UTC()

	var next *Job

	busy := false

	err = sc.store.update(func(data *snapshot) error {
		for i := range data.Jobs {
			job := &data.Jobs[i]
			if job.DeviceID != deviceID {
				continue
			}

			switch job.Status {
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

		if err != nil || busy || data.Paused[deviceID] || !device.Connected || device.Retired || state != printerReady {
			return nil
		}

		for i := range data.Jobs {
			job := &data.Jobs[i]
			if job.DeviceID != deviceID || job.Status != statusQueued {
				continue
			}

			if job.NotBefore != nil && job.NotBefore.After(now) {
				continue
			}

			// Written down before the printer is asked, so a restart in
			// between never sends the same job twice.
			job.Status = statusStarting
			job.StartedAt = &now
			job.setDetail("")
			copied := *job
			next = &copied

			return nil
		}

		return nil
	})
	if err != nil {
		sc.logf("save the queue: %v", err)

		return
	}

	if next != nil {
		sc.start(ctx, *next)
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
func (sc *Scheduler) start(ctx context.Context, job Job) {
	params := deviceapi.PrintParams{ID: job.DeviceID, Path: job.Path}
	if job.Storage != "" {
		params.Storage = job.Storage
		params.Source = job.Path
		// The SDK returns the transfer ID before it knows the command ID.
		// A fresh destination lets the event stream tie the two together.
		name := path.Base(job.Path)
		params.Path = path.Join(sc.folder, newID()+"-"+name)
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
		current.PrinterPath = params.Path
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
