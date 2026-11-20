package main

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
)

const (
	plateDelay        = 30 * time.Second
	plateSampleWindow = 2 * plateDelay
	platePermitTTL    = 40 * time.Second
	readyVerb         = "core.command.set_printer_ready"
)

type plateSettings struct {
	Device      string              `json:"device"`
	Camera      string              `json:"camera"`
	EnabledBy   string              `json:"enabled_by,omitempty"`
	Reference   []byte              `json:"reference,omitempty"`
	ReferenceAt time.Time           `json:"reference_at"`
	Profiles    []savedPlateProfile `json:"profiles,omitempty"`
}

type plateView struct {
	Automatic    bool                  `json:"automatic"`
	ReferenceAt  *time.Time            `json:"reference_at,omitempty"`
	LastMatchAt  *time.Time            `json:"last_match_at,omitempty"`
	Status       string                `json:"status"`
	CanConfigure bool                  `json:"can_configure"`
	ProfileCount int                   `json:"profile_count,omitempty"`
	Calibration  *plateCalibrationView `json:"calibration,omitempty"`
}

type plateCheck struct {
	settings           plateSettings
	file               string
	reference          image.Image
	profiles           []plateProfile
	calibration        *plateCalibration
	calibrationFailure string
	status             string
	lastMatch          *time.Time
	first              plateSample
	permit             platePermit
	snapshot           func(context.Context) (plateFrame, error)
	mayEnable          func(context.Context, string, string) bool
}

type plateSample struct {
	job     string
	window  plateWindow
	epoch   uint64
	checked time.Time
	taken   time.Time
	sum     [32]byte
	matches uint16
}

type plateWindow struct {
	state    string
	previous string
	ended    time.Time
}

type platePermit struct {
	job     string
	epoch   uint64
	expires time.Time
}

// plateReady is persisted before issuing READY. A process restart loses the
// in-memory permit and therefore cannot start a job using that READY state.
type plateReady struct {
	Seen bool `json:"seen"`
}

func openPlateCheck(dir string, settings Settings, now func() time.Time) (*plateCheck, error) {
	if settings.PlateCheckEnabled != nil && !*settings.PlateCheckEnabled {
		return nil, nil
	}
	if settings.PlatePrinter == "" && settings.PlateCameraURL == "" && settings.PlateCameraStream == "" {
		return nil, nil
	}
	if settings.PlatePrinter == "" {
		return nil, errors.New("plate_check_printer is required with a plate camera")
	}
	p := &plateCheck{file: filepath.Join(dir, "plate-check.json"), status: "noReference"}
	p.settings.Device = settings.PlatePrinter
	if settings.PlateCameraStream != "" {
		address, peer, err := plateRTSPAddress(settings.PlateCameraStream)
		if err != nil {
			return nil, err
		}
		p.settings.Camera = address.String()
		gate := make(chan struct{}, 1)
		p.snapshot = func(ctx context.Context) (plateFrame, error) {
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
				return readPlateRTSP(ctx, address, peer, now)
			default:
				return plateFrame{}, errors.New("the RTSP camera is already capturing an image")
			}
		}
	} else {
		camera, err := newPlateCamera(settings.PlateCameraURL, filepath.Join(dir, "plate-camera.token"), now)
		if err != nil {
			return nil, err
		}
		camera.token = settings.PlateCameraToken
		p.settings.Camera, p.snapshot = camera.url, camera.snapshot
	}
	if err := p.load(); err != nil {
		// The queue remains available for manual READY if plate settings are unreadable.
		return p, err
	}
	return p, nil
}

func (p *plateCheck) load() error {
	raw, err := os.ReadFile(p.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved plateSettings
	if err := json.Unmarshal(raw, &saved); err != nil {
		return errors.New("invalid saved plate settings")
	}
	if saved.Device != p.settings.Device || saved.Camera != p.settings.Camera {
		return nil
	}
	var ref image.Image
	if len(saved.Reference) != 0 {
		ref, err = decodePlateJPEG(saved.Reference)
		if err != nil || saved.ReferenceAt.IsZero() || !usablePlateImage(ref) {
			return errors.New("invalid saved plate reference")
		}
	}
	profiles, err := decodePlateProfiles(saved.Profiles)
	if err != nil {
		return err
	}
	if ref == nil && len(profiles) == 0 {
		saved.EnabledBy = ""
	}
	p.settings, p.reference, p.profiles = saved, ref, profiles
	if p.hasReference() {
		p.status = "waiting"
	}
	return nil
}

func (p *plateCheck) save(next plateSettings) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tmp := p.file + ".tmp"
	if err = os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p.file); err != nil {
		return err
	}
	p.settings = next
	return nil
}

func (p *plateCheck) reset(status string) {
	p.first = plateSample{}
	p.status = status
}

func platePrinterAvailable(device deviceapi.Device) bool {
	return device.Connected && !device.Retired && device.Dialog == nil && len(device.Issues) == 0
}

func platePrinterCanCheck(device deviceapi.Device, state string) bool {
	return platePrinterAvailable(device) && (state == "FINISHED" || state == "IDLE") &&
		slices.Contains(device.Capabilities, readyVerb)
}

func (sc *Scheduler) lastPlatePrint(ctx context.Context, id string) (*deviceapi.Job, error) {
	jobs, err := sc.fleet.Jobs(ctx, deviceapi.JobsParams{ID: id})
	if err != nil || (jobs.Current != nil && jobs.Current.Ended == nil) {
		return nil, errors.New("printer jobs could not confirm an empty window")
	}
	return jobs.Last, nil
}

func (sc *Scheduler) plateWindow(ctx context.Context, id, state string) (plateWindow, bool) {
	last, err := sc.lastPlatePrint(ctx, id)
	if err != nil {
		return plateWindow{}, false
	}
	if last == nil && state == "IDLE" {
		return plateWindow{state: state}, true
	}
	if last == nil || last.ID == "" || last.Ended == nil || last.Outcome != event.JobFinished {
		return plateWindow{}, false
	}
	return plateWindow{state: state, previous: last.ID, ended: last.Ended.UTC()}, true
}

func (sc *Scheduler) checkPlate(ctx context.Context, device deviceapi.Device, next Job) {
	p := sc.plate
	if !p.hasReference() {
		p.reset("noReference")
		return
	}
	state := printerState(device)
	if !platePrinterCanCheck(device, state) {
		p.reset("waiting")
		return
	}
	window, ok := sc.plateWindow(ctx, next.DeviceID, state)
	if !ok {
		p.reset("waiting")
		return
	}
	first := p.first
	if first.job != next.ID || first.window != window || first.epoch != device.CapabilityEpoch {
		first = plateSample{}
	}
	if !first.checked.IsZero() && sc.now().Sub(first.checked) < plateDelay {
		return
	}
	shot, err := p.snapshot(ctx)
	checked := sc.now()
	if !first.checked.IsZero() && checked.Sub(first.checked) > plateSampleWindow {
		first = plateSample{}
	}
	p.first = first
	if err != nil || checked.Sub(shot.taken) > plateMaxAge || !shot.taken.After(window.ended) ||
		(!first.checked.IsZero() && shot.sum == first.sum) {
		p.status = "cameraUnavailable"
		if !first.checked.IsZero() {
			p.status = "retryingCamera"
		}
		return
	}
	matches := p.matchingProfiles(state, shot.image)
	if matches == 0 {
		p.reset("different")
		return
	}
	if !first.checked.IsZero() && shot.taken.Sub(first.taken) < plateDelay {
		p.status = "retryingCamera"
		return
	}
	if first.matches&matches == 0 {
		first = plateSample{}
	}
	if first.checked.IsZero() {
		p.first = plateSample{
			job: next.ID, window: window, epoch: device.CapabilityEpoch,
			checked: checked, taken: shot.taken, sum: shot.sum, matches: matches,
		}
		p.status = "firstMatch"
		return
	}
	p.reset("matched")
	p.lastMatch = &shot.taken
	if p.settings.EnabledBy == "" {
		// Start a fresh pair on the next tick, even in observation mode.
		return
	}
	if !p.mayEnable(ctx, p.settings.EnabledBy, next.DeviceID) || sc.authorize(ctx, next) != nil {
		p.status = "permissionRequired"
		return
	}
	currentWindow, ok := sc.plateWindow(ctx, next.DeviceID, state)
	if !ok || currentWindow != window {
		p.status = "waiting"
		return
	}
	currentDevice, err := sc.fleet.Get(ctx, deviceapi.GetParams{ID: next.DeviceID})
	if err != nil || printerState(currentDevice) != state || !platePrinterCanCheck(currentDevice, state) ||
		currentDevice.CapabilityEpoch != device.CapabilityEpoch ||
		sc.now().Sub(shot.taken) > plateMaxAge || ctx.Err() != nil {
		p.status = "waiting"
		return
	}

	armed := false
	if err := sc.store.update(func(data *snapshot) error {
		job := data.nextQueued(next.DeviceID, sc.now())
		if data.Paused[next.DeviceID] || job == nil || job.ID != next.ID {
			return nil
		}
		if _, exists := data.PlateReady[next.DeviceID]; exists {
			return nil
		}
		if data.PlateReady == nil {
			data.PlateReady = map[string]plateReady{}
		}
		data.PlateReady[next.DeviceID] = plateReady{}
		armed = true
		return nil
	}); err != nil {
		p.status = "readyBlocked"
		sc.logf("save automatic READY guard on %s: %v", next.DeviceID, err)
		return
	}
	if !armed {
		p.status = "waiting"
		return
	}
	if err := sc.sendReady(ctx, next.DeviceID); err != nil {
		// Keep the guard. The command may have reached the printer.
		p.status = "readyBlocked"
		return
	}
	p.permit = platePermit{job: next.ID, epoch: device.CapabilityEpoch, expires: shot.taken.Add(platePermitTTL)}
	p.status = "waitingReady"
}

func (p *plateCheck) capture(ctx context.Context, sc *Scheduler) (plateSettings, image.Image, error) {
	shot, _, err := p.referenceFrame(ctx, sc, true)
	if err != nil {
		return plateSettings{}, nil, err
	}
	next := p.settings
	next.Reference, next.ReferenceAt = shot.jpeg, shot.taken
	next.EnabledBy = ""
	return next, shot.image, nil
}

func (p *plateCheck) referenceFrame(ctx context.Context, sc *Scheduler, allowReady bool) (plateFrame, plateCalibrationContext, error) {
	id := p.settings.Device
	before, err := sc.fleet.Get(ctx, deviceapi.GetParams{ID: id})
	state := printerState(before)
	if err != nil || !platePrinterAvailable(before) ||
		(state != "IDLE" && state != "FINISHED" && !(allowReady && state == printerReady)) {
		return plateFrame{}, plateCalibrationContext{}, errors.New("the printer must be stopped and available to capture a reference")
	}
	shot, err := p.snapshot(ctx)
	if err != nil {
		return plateFrame{}, plateCalibrationContext{}, err
	}
	last, err := sc.lastPlatePrint(ctx, id)
	if err != nil || (last != nil && last.Ended != nil && !shot.taken.After(*last.Ended)) {
		return plateFrame{}, plateCalibrationContext{}, errors.New("wait for the finished print and a new camera image")
	}
	after, err := sc.fleet.Get(ctx, deviceapi.GetParams{ID: id})
	if err != nil || !platePrinterAvailable(after) || printerState(after) != state ||
		after.CapabilityEpoch != before.CapabilityEpoch || sc.now().Sub(shot.taken) > plateMaxAge {
		return plateFrame{}, plateCalibrationContext{}, errors.New("the printer changed state during capture")
	}
	if !usablePlateImage(shot.image) {
		return plateFrame{}, plateCalibrationContext{}, errors.New("the camera image needs more contrast and light")
	}
	capture := plateCalibrationContext{state: state, epoch: before.CapabilityEpoch}
	if last != nil {
		capture.previous = last.ID
		if last.Ended != nil {
			capture.ended = last.Ended.UTC()
		}
	}
	return shot, capture, nil
}
