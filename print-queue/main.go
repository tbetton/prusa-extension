// Command print-queue is a Prusa Connect module that keeps a queue of prints
// for each printer. When a printer reports READY (the plate was cleared and
// confirmed on the printer), it sends the next queued file from the server's
// storage and starts it. A job may also wait for a given time.
//
// The code is split by what it does:
//
//	main.go           the process, its settings and the HTTP routes of the queue
//	queue.go          the jobs, their storage on disk, and the scheduler
//	print_events.go   follows the host's events to track each print
//	print_history.go  settles jobs from the printer's job history
//	ready.go          marks a printer ready by hand
//	inbox.go          the files PrusaSlicer left for a printer
//	faults.go         the refusals the routes answer
//
// The module runs as a child process of Prusa Connect and talks to it over its
// standard input and output. Every request carries the person it is made for
// (the principal), and the module asks the host whether that person may act.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

const (
	name    = "print-queue"
	release = extension.Version("0.3.4")
)

// Settings is what an operator writes under this instance's config block. The
// defaults live in module.yaml, and withDefaults repeats them so a missing key
// never reaches the code as a zero.
type Settings struct {
	PollSeconds         int    `json:"poll_seconds"`
	StartTimeoutMinutes int    `json:"start_timeout_minutes"`
	PrinterFolder       string `json:"printer_folder"`
	KeepHistory         int    `json:"keep_history"`
	InboxStorage        string `json:"inbox_storage"`
	InboxFolder         string `json:"inbox_folder"`
	// Prestage copies the next file onto the printer while it is not ready.
	// It is a pointer so that a missing key means on and not off.
	Prestage *bool `json:"prestage"`
}

func (s Settings) withDefaults() Settings {
	if s.PollSeconds <= 0 {
		s.PollSeconds = 10
	}

	if s.StartTimeoutMinutes <= 0 {
		s.StartTimeoutMinutes = 30
	}

	s.PrinterFolder = strings.Trim(s.PrinterFolder, "/")
	if s.PrinterFolder == "" {
		s.PrinterFolder = "usb"
	}

	if s.Prestage == nil {
		on := true
		s.Prestage = &on
	}

	s.InboxStorage = strings.TrimSpace(s.InboxStorage)
	if s.InboxStorage == "" {
		s.InboxStorage = "local"
	}

	s.InboxFolder = strings.Trim(strings.TrimSpace(s.InboxFolder), "/")
	if s.InboxFolder == "" {
		s.InboxFolder = "print-queue"
	}

	if s.KeepHistory < 0 {
		s.KeepHistory = 0
	}

	return s
}

// openAPI is the description of this module's routes, served to the host so it
// can publish them in its own API reference.
//
//go:embed openapi.json
var openAPI []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

// module ties the session, the store and the scheduler together.
type module struct {
	mu        sync.Mutex
	access    accessapi.Access
	conn      *wirerpc.Conn
	store     *Store
	devices   *deviceapi.Client
	files     *storageapi.Client
	scheduler *Scheduler
	ctx       context.Context

	// The storage and the folder PrusaSlicer's uploads are saved in, one
	// folder for each printer under it.
	inboxStorage string
	inboxFolder  string
	list         func(ctx context.Context, storage, folder string) ([]storageapi.ListEntry, error)
	inboxLogged  time.Time
}

// logf writes a line to the module's log, which the host keeps with its own.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, name+": "+format+"\n", args...)
}

// run starts the session with the host: it announces the protocol on standard
// output, builds the clients the module uses (printers, storage, access), and
// serves the host's calls until the process is asked to stop. The module does
// nothing useful until the host sends its settings (see configure).
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return fmt.Errorf("render the handshake: %w", err)
	}

	if _, err = os.Stdout.WriteString(line); err != nil {
		return fmt.Errorf("write the handshake: %w", err)
	}

	m := &module{ctx: ctx}

	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{
		Role:    wirerpc.RoleChild,
		Methods: m.methods(),
	})
	if err != nil {
		return fmt.Errorf("build the session: %w", err)
	}

	m.conn = conn
	m.access = accessapi.NewClient(conn)
	m.devices = deviceapi.NewClient(conn)
	m.files = storageapi.NewClient(conn)
	m.list = m.listFolder

	logf("%s is serving on stdio as pid %d", release, os.Getpid())

	if err = conn.Serve(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

// methods lists the calls the host may make to this module: its description,
// its settings, the request to stop, and the HTTP routes.
func (m *module) methods() wirerpc.Methods {
	return wirerpc.Methods{
		wirerpc.MethodInfo: func(context.Context, wirerpc.Call) (any, error) {
			return routesapi.Describe(
				wirerpc.Info{
					Name:     name,
					Version:  release,
					Contract: wirerpc.Contract,
					Serves:   []wirerpc.Method{wirerpc.MethodConfigure},
				}, openAPI)
		},
		wirerpc.MethodConfigure: wirerpc.ConfigureGranted(m.configure),
		wirerpc.MethodShutdown: func(context.Context, wirerpc.Call) (any, error) {
			logf("asked to stop")

			return nil, nil
		},
		routesapi.MethodServe: routesapi.Serve(m.routes()),
	}
}

// configure opens the store in the data directory and starts the loop.
func (m *module) configure(_ context.Context, sent Settings, grants wirerpc.Grants) error {
	settings := sent.withDefaults()
	if !fs.ValidPath(settings.InboxFolder) || settings.InboxFolder == "." ||
		strings.ContainsAny(settings.InboxFolder, "\\") || strings.Contains(settings.InboxStorage, "/") {
		return errors.New("print-queue: inbox_folder must be a folder of the storage and inbox_storage a storage name")
	}
	if int64(settings.PollSeconds) > math.MaxInt64/int64(3*time.Second) ||
		int64(settings.StartTimeoutMinutes) > math.MaxInt64/int64(time.Minute) {
		return errors.New("print-queue: a configured duration is too long")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store != nil {
		return errors.New("print-queue: the settings were sent twice")
	}

	if grants.Data == "" {
		return errors.New("print-queue: a persistent data directory is required")
	}

	store, err := OpenStore(grants.Data, time.Now, settings.KeepHistory)
	if err != nil {
		return fmt.Errorf("print-queue: %w", err)
	}

	m.store = store
	m.inboxStorage = settings.InboxStorage
	m.inboxFolder = settings.InboxFolder
	m.scheduler = &Scheduler{
		store:        store,
		fleet:        m.devices,
		now:          time.Now,
		prestage:     *settings.Prestage,
		startTimeout: time.Duration(settings.StartTimeoutMinutes) * time.Minute,
		folder:       settings.PrinterFolder,
		logf:         logf,
		authorize:    m.authorizeDispatch,
		command:      m.devices.Command,
	}
	go m.scheduler.follow(m.ctx, m.conn)
	go m.loop(time.Duration(settings.PollSeconds) * time.Second)

	return nil
}

// loop runs, every `every`, one pass of the scheduler (start what is ready and
// settle what the printers finished) and one pass of the inbox (add the new
// files of the printers set to add them by themselves). Each pass has a time
// limit of three intervals, so a slow host cannot pile passes up.
func (m *module) loop(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(m.ctx, every*3)
			m.scheduler.Tick(ctx)
			m.autoAdd(ctx)
			cancel()
		}
	}
}

// ready answers the store, or refuses the request while the module waits for
// its settings.
func (m *module) ready(w http.ResponseWriter, r *http.Request) *Store {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		refuse(w, r, NotReady.New("the module has not been configured yet"))

		return nil
	}

	return m.store
}

// addBody is the body of POST /jobs. An empty Storage means the file is already
// on the printer's own drive.
type addBody struct {
	DeviceID  string     `json:"device_id"`
	Storage   string     `json:"storage"`
	Path      string     `json:"path"`
	Name      string     `json:"name,omitempty"`
	NotBefore *time.Time `json:"not_before,omitempty"`
}

type moveBody struct {
	Direction string `json:"direction"`
}

type scheduleBody struct {
	NotBefore *time.Time `json:"not_before"`
}

type pausedBody struct {
	Paused bool `json:"paused"`
}

type clearBody struct {
	DeviceID string `json:"device_id"`
}

// routes builds the HTTP routes of the queue. Each one first asks the host
// whether the caller may see (observe) or change (operate) the printer, and a
// route that adds a file also checks the caller may read it.
func (m *module) routes() http.Handler {
	mux := http.NewServeMux()

	// GET /jobs: the queue of one printer (?device=), or of every printer the
	// caller may observe.
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		store := m.ready(w, r)
		if store == nil {
			return
		}

		deviceID := r.URL.Query().Get("device")
		principal := routesapi.PrincipalOf(r)
		if deviceID != "" && !m.allowed(r.Context(), principal, "observe", "device", deviceID) {
			refuse(w, r, NoDevice.New("the printer is unavailable"))
			return
		}
		view := store.List(deviceID)
		if deviceID == "" {
			visible := make(map[string]bool)
			for _, id := range view.deviceIDs() {
				visible[id] = m.allowed(r.Context(), principal, "observe", "device", id)
			}
			view.Jobs = slices.DeleteFunc(view.Jobs, func(job Job) bool { return !visible[job.DeviceID] })
			for id := range view.Paused {
				if !visible[id] {
					delete(view.Paused, id)
				}
			}
			for id := range view.ReadyConfirmationRequired {
				if !visible[id] {
					delete(view.ReadyConfirmationRequired, id)
				}
			}
		}
		answer(w, http.StatusOK, view)
	})

	mux.HandleFunc("POST /devices/{id}/ready", m.markReady)

	m.registerInbox(mux)

	// POST /jobs: add a file to the end of a printer's queue. The caller must
	// operate the printer and, for a file in storage, read that file.
	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		store := m.ready(w, r)
		if store == nil {
			return
		}

		var body addBody
		if !decode(w, r, &body) {
			return
		}

		body.Path = strings.TrimPrefix(body.Path, "/")
		if body.DeviceID == "" || body.Path == "" {
			refuse(w, r, BadRequest.New("device_id and path are required"))

			return
		}

		principal := routesapi.PrincipalOf(r)

		if !m.requireOperate(w, r, body.DeviceID) {
			return
		}
		device, err := m.devices.Get(r.Context(), deviceapi.GetParams{Principal: principal, ID: body.DeviceID})
		if err != nil || device.Retired || (device.Kind != "" && device.Kind != "printer") {
			refuse(w, r, NoDevice.New("the printer is unavailable"))
			return
		}

		if params := (deviceapi.PrintParams{ID: body.DeviceID, Path: body.Path, Storage: body.Storage}); body.Storage != "" {
			params.Source = body.Path
			params.Path = path.Base(body.Path)
			if params.Validate() != nil {
				refuse(w, r, BadRequest.New("invalid print source"))
				return
			}
		} else if params.Validate() != nil {
			refuse(w, r, BadRequest.New("invalid printer path"))
			return
		}

		// An empty storage is a file the printer already holds, on its own
		// drive: the printer is asked for it when the job starts.
		if body.Storage != "" {
			if _, err := m.files.Stat(r.Context(), storageapi.StatParams{
				Principal: principal, Storage: body.Storage, Path: body.Path,
			}); err != nil {
				refuse(w, r, NoFile.New("the file is unavailable"))

				return
			}
		}

		job, err := store.Add(Job{
			DeviceID: body.DeviceID, Storage: body.Storage, Path: body.Path, Name: body.Name,
			AddedBy: principal, NotBefore: body.NotBefore,
		})
		if err != nil {
			logf("add a job: %v", err)
			refuse(w, r, StoreFailed.New("the queue could not be saved"))

			return
		}

		answer(w, http.StatusCreated, job)
	})

	// DELETE /jobs/{id}: take a waiting or ended job out of the queue.
	mux.HandleFunc("DELETE /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.changeJob(w, r, func(store *Store, job Job) error { return store.Remove(job.ID) })
	})

	// POST /jobs/{id}/move: swap a waiting job with its neighbour, up or down.
	mux.HandleFunc("POST /jobs/{id}/move", func(w http.ResponseWriter, r *http.Request) {
		var body moveBody
		if !decode(w, r, &body) {
			return
		}

		if body.Direction != "up" && body.Direction != "down" {
			refuse(w, r, BadRequest.New("direction is up or down"))

			return
		}

		m.changeJob(w, r, func(store *Store, job Job) error { return store.Move(job.ID, body.Direction == "up") })
	})

	// PUT /jobs/{id}/schedule: hold a waiting job until a time, or release it.
	mux.HandleFunc("PUT /jobs/{id}/schedule", func(w http.ResponseWriter, r *http.Request) {
		var body scheduleBody
		if !decode(w, r, &body) {
			return
		}

		m.changeJob(w, r, func(store *Store, job Job) error { return store.Schedule(job.ID, body.NotBefore) })
	})

	// POST /jobs/{id}/requeue: put an ended job back at the end of the queue,
	// now owned by the caller, who must still be able to read its file.
	mux.HandleFunc("POST /jobs/{id}/requeue", func(w http.ResponseWriter, r *http.Request) {
		m.changeJob(w, r, func(store *Store, job Job) error {
			principal := routesapi.PrincipalOf(r)
			if job.Storage != "" && !m.allowed(r.Context(), principal, "read_storage", "resource_kind", "storage") {
				return errOperationDenied
			}
			return store.Requeue(job.ID, principal)
		})
	})

	// POST /jobs/clear-finished: forget the ended jobs of one printer, or of
	// every printer the caller may operate.
	mux.HandleFunc("POST /jobs/clear-finished", func(w http.ResponseWriter, r *http.Request) {
		var body clearBody
		if !decode(w, r, &body) {
			return
		}

		store := m.ready(w, r)
		if store == nil {
			return
		}
		allowed := make(map[string]bool)
		if body.DeviceID != "" {
			if !m.requireOperate(w, r, body.DeviceID) {
				return
			}
			allowed[body.DeviceID] = true
		} else {
			principal := routesapi.PrincipalOf(r)
			for _, id := range store.List("").deviceIDs() {
				allowed[id] = m.allowed(r.Context(), principal, "operate", "device", id)
			}
		}
		m.writeResult(w, r, store.ClearFinished(allowed))
	})

	// PUT /devices/{id}/paused: hold a printer's queue, or let it run again.
	mux.HandleFunc("PUT /devices/{id}/paused", func(w http.ResponseWriter, r *http.Request) {
		var body pausedBody
		if !decode(w, r, &body) {
			return
		}

		store := m.ready(w, r)
		if store != nil && m.requireOperate(w, r, r.PathValue("id")) {
			m.writeResult(w, r, store.SetPaused(r.PathValue("id"), body.Paused))
		}
	})

	// Any other path.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		refuse(w, r, RouteNotFound.
			New("this module serves no route at "+r.URL.Path).
			With(fault.StringParam("path", r.URL.Path)))
	})

	return mux
}

var errOperationDenied = errors.New("permission denied for this operation")

// check asks the host whether a person may do an action on an object, for
// example `operate` on a printer. It answers errOperationDenied for a refusal
// and for a call with no person, and the host's own error when it cannot answer.
func (m *module) check(ctx context.Context, principal, action, objectType, id string) error {
	if principal == "" {
		return errOperationDenied
	}
	decision, err := m.access.Check(ctx, accessapi.CheckParams{
		Subject: principal, Action: action, ObjectType: objectType, ObjectID: id,
	})
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return errOperationDenied
	}
	return nil
}

// allowed is check as a yes or no. A question the host could not answer counts
// as no.
func (m *module) allowed(ctx context.Context, principal, action, objectType, id string) bool {
	return m.check(ctx, principal, action, objectType, id) == nil
}

// requireOperate answers true when the caller may operate the printer. It
// writes the refusal itself otherwise, so a route just returns on false.
func (m *module) requireOperate(w http.ResponseWriter, r *http.Request, deviceID string) bool {
	if m.allowed(r.Context(), routesapi.PrincipalOf(r), "operate", "device", deviceID) {
		return true
	}
	refuse(w, r, OperationDenied.New("you cannot operate this printer"))
	return false
}

// authorizeDispatch is the check made when a job is about to start, as the
// person who queued it: they must still be able to operate the printer and, for
// a file in storage, to read the storage.
func (m *module) authorizeDispatch(ctx context.Context, job Job) error {
	if err := m.check(ctx, job.AddedBy, "operate", "device", job.DeviceID); err != nil {
		return err
	}
	if job.Storage != "" {
		return m.check(ctx, job.AddedBy, "read_storage", "resource_kind", "storage")
	}
	return nil
}

// changeJob runs a change on one job of the URL after checking that the caller
// can see the job's printer and may operate it, and answers the change's result.
func (m *module) changeJob(w http.ResponseWriter, r *http.Request, write func(*Store, Job) error) {
	store := m.ready(w, r)
	if store == nil {
		return
	}

	job, found := store.job(r.PathValue("id"))
	if !found || !m.allowed(r.Context(), routesapi.PrincipalOf(r), "observe", "device", job.DeviceID) {
		refuse(w, r, JobNotFound.New("no such job"))
		return
	}
	if m.requireOperate(w, r, job.DeviceID) {
		m.writeResult(w, r, write(store, job))
	}
}

// writeResult turns the error of a change into the matching refusal, or a 204
// when there is none.
func (m *module) writeResult(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errOperationDenied):
		refuse(w, r, OperationDenied.New("you do not have the required permission"))
	case errors.Is(err, errNoJob):
		refuse(w, r, JobNotFound.New("no such job"))
	case errors.Is(err, errJobRunning):
		refuse(w, r, JobRunning.New("the job is being printed or is not waiting"))
	default:
		logf("save the queue: %v", err)
		refuse(w, r, StoreFailed.New("the queue could not be saved"))
	}
}

// decode reads a JSON body of at most 64 KiB, and refuses the request itself
// when it cannot.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(into); err != nil {
		refuse(w, r, BadRequest.New("the body is not the JSON this route reads: "+err.Error()))

		return false
	}

	return true
}

// answer writes a JSON body with a status.
func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(body)
}

// stdio is the host's channel: standard input and output of the process.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }
