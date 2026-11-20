package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
)

type plateFleet struct {
	*fakeFleet
	epoch uint64
}

func (f *plateFleet) Get(ctx context.Context, params deviceapi.GetParams) (deviceapi.Device, error) {
	d, err := f.fakeFleet.Get(ctx, params)
	d.Capabilities = []string{readyVerb}
	d.CapabilityEpoch = f.epoch
	return d, err
}

func platePhoto(level uint8, obstruction bool) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, 320, 240))
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			value := level
			if x < 48 || (obstruction && x >= 145 && x < 152 && y >= 144 && y < 155) {
				value = 155
			}
			img.SetGray(x, y, color.Gray{Y: value})
		}
	}
	return img
}

func encodeTestJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	return jpegBytes.Bytes()
}

func TestPlateComparisonRejectsLocalObstruction(t *testing.T) {
	ref := platePhoto(45, false)
	if !usablePlateImage(ref) || !plateMatch(ref, platePhoto(49, false)) {
		t.Fatal("a small lighting variation in the empty view should match")
	}
	obstructed, err := decodePlateJPEG(encodeTestJPEG(t, platePhoto(45, true)))
	if err != nil {
		t.Fatal(err)
	}
	if plateMatch(ref, obstructed) || plateMatch(ref, image.NewGray(image.Rect(0, 0, 640, 480))) {
		t.Fatal("a local obstruction or a different camera resolution must not match")
	}
	if usablePlateImage(image.NewGray(ref.Bounds())) {
		t.Fatal("a black frame is unusable")
	}
}

func TestPlateRTSPAddressIsScopedToTheSelectedLANCamera(t *testing.T) {
	for _, address := range []string{
		"rtsp://10.0.10.153/live",
		"rtsp://192.168.1.100:8554/live",
		"rtsp://[fd00::1234]/live",
	} {
		if _, _, err := plateRTSPAddress(address); err != nil {
			t.Errorf("expected LAN address %s: %v", address, err)
		}
	}
	for _, address := range []string{
		"rtsp://user:secret@10.0.10.153/live",
		"rtsp://10.0.10.153/live?token=secret",
		"rtsp://camera.example/live",
		"rtsp://10.0.10.153:22/live",
		"rtsp://203.0.113.1/live",
		"rtsp://127.0.0.1/live",
		"http://10.0.10.153/live",
	} {
		if _, _, err := plateRTSPAddress(address); err == nil {
			t.Errorf("unexpected valid RTSP address %s", address)
		}
	}
}

func TestRTSPComparisonSuppressesNoiseButRejectsALocalChange(t *testing.T) {
	reference := image.NewGray(image.Rect(0, 0, 1280, 720))
	noisy := image.NewGray(reference.Bounds())
	for y := range 720 {
		for x := range 1280 {
			level := uint8(55)
			if x < 100 {
				level = 150
			}
			reference.SetGray(x, y, color.Gray{Y: level})
			noisy.SetGray(x, y, color.Gray{Y: level})
		}
	}
	for y := 368; y < 384; y++ {
		for x := 448; x < 464; x++ {
			level := uint8(43)
			if (x+y)%2 == 0 {
				level = 67
			}
			noisy.SetGray(x, y, color.Gray{Y: level})
		}
	}
	referenceImage := plateRTSPComparison(reference)
	if referenceImage.Bounds().Dx() != 640 || referenceImage.Bounds().Dy() != 360 ||
		plateMatch(reference, noisy) || !plateMatch(referenceImage, plateRTSPComparison(noisy)) {
		t.Fatal("the comparison should average fine RTSP noise")
	}
	for y := 288; y < 310; y++ {
		for x := 290; x < 304; x++ {
			noisy.SetGray(x, y, color.Gray{Y: 150})
		}
	}
	if plateMatch(referenceImage, plateRTSPComparison(noisy)) {
		t.Fatal("the comparison must reject a small obstruction after averaging")
	}
}

func TestPlateCheckAutomaticAndObservation(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "observation"
		if automatic {
			name = "automatic"
		}
		t.Run(name, func(t *testing.T) {
			store, fleet, sc, clock := setup(t, "FINISHED")
			commands, samples := configurePlateFixture(t, fleet, sc, clock, automatic)
			_, _ = store.Add(Job{DeviceID: "p1", Path: "next.bgcode"})
			tick := func(after time.Duration) {
				clock.at = clock.at.Add(after)
				sc.Tick(t.Context())
			}
			tick(0)
			tick(29 * time.Second)
			if *samples != 1 || *commands != 0 || len(fleet.printed) != 0 {
				t.Fatal("more than one capture or dispatch before the 30-second interval")
			}
			tick(time.Second)
			wantCommands := 0
			if automatic {
				wantCommands = 1
			}
			if *samples != 2 || sc.plate.lastMatch == nil || *commands != wantCommands || len(fleet.printed) != 0 {
				t.Fatalf("after the second capture: samples=%d commands=%d prints=%d status=%s", *samples, *commands, len(fleet.printed), sc.plate.status)
			}
			tick(5 * time.Second)
			if len(fleet.printed) != 0 {
				t.Fatal("dispatched while printer still reported FINISHED")
			}
			fleet.state = printerReady
			tick(5 * time.Second)
			if len(fleet.printed) != 1 || fleet.printed[0].Path != "next.bgcode" {
				t.Fatalf("expected exactly one job after READY: %+v", fleet.printed)
			}
		})
	}
}

func TestPlateCheckRejectsAnObstructionOrMissingEvidence(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*Store, *fakeFleet, *Scheduler)
	}{
		{"obstruction", func(_ *Store, _ *fakeFleet, sc *Scheduler) {
			get := sc.plate.snapshot
			sc.plate.snapshot = func(ctx context.Context) (plateFrame, error) {
				shot, err := get(ctx)
				shot.image = platePhoto(45, true)
				return shot, err
			}
		}},
		{"no camera frame", func(_ *Store, _ *fakeFleet, sc *Scheduler) {
			sc.plate.snapshot = func(context.Context) (plateFrame, error) {
				return plateFrame{}, errors.New("camera unavailable")
			}
		}},
		{"cached JPEG", func(_ *Store, _ *fakeFleet, sc *Scheduler) {
			get := sc.plate.snapshot
			sc.plate.snapshot = func(ctx context.Context) (plateFrame, error) {
				shot, err := get(ctx)
				shot.sum = sc.plate.first.sum
				return shot, err
			}
		}},
		{"reconnected", func(_ *Store, _ *fakeFleet, sc *Scheduler) {
			sc.fleet.(*plateFleet).epoch++
		}},
		{"state changed", func(_ *Store, fleet *fakeFleet, _ *Scheduler) {
			fleet.state = "IDLE"
		}},
		{"paused", func(store *Store, _ *fakeFleet, _ *Scheduler) {
			_ = store.SetPaused("p1", true)
		}},
		{"failed last print", func(_ *Store, fleet *fakeFleet, _ *Scheduler) {
			fleet.recent.Last.Outcome = event.JobFailed
		}},
		{"printer busy", func(_ *Store, fleet *fakeFleet, _ *Scheduler) {
			fleet.recent.Current = &deviceapi.Job{ID: "current"}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, fleet, sc, clock := setup(t, "FINISHED")
			commands, _ := configurePlateFixture(t, fleet, sc, clock, true)
			_, _ = store.Add(Job{DeviceID: "p1", Path: "next.bgcode"})
			sc.Tick(t.Context())
			clock.at = clock.at.Add(plateDelay)
			scenario.change(store, fleet, sc)
			sc.Tick(t.Context())
			if *commands != 0 || len(fleet.printed) != 0 {
				t.Fatal("automatic readiness must remain on hold")
			}
		})
	}
}

func TestPlateCheckStartsFromIdle(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		update   func(*fakeFleet)
		canStart bool
	}{
		{name: "previous print finished", canStart: true},
		{name: "no previous print", update: func(f *fakeFleet) { f.recent.Last = nil }, canStart: true},
		{name: "previous print failed", update: func(f *fakeFleet) { f.recent.Last.Outcome = event.JobFailed }},
		{name: "active print", update: func(f *fakeFleet) { f.recent.Current = &deviceapi.Job{ID: "current"} }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, fleet, sc, clock := setup(t, "IDLE")
			commands, _ := configurePlateFixture(t, fleet, sc, clock, true)
			if scenario.update != nil {
				scenario.update(fleet)
			}
			_, _ = store.Add(Job{DeviceID: "p1", Path: "next.bgcode"})
			sc.Tick(t.Context())
			if *commands != 0 {
				t.Fatal("one camera capture cannot authorize READY")
			}
			clock.at = clock.at.Add(plateDelay)
			sc.Tick(t.Context())
			if !scenario.canStart {
				if *commands != 0 || len(fleet.printed) != 0 {
					t.Fatal("IDLE alone must not authorize READY")
				}
				return
			}
			if *commands != 1 || len(fleet.printed) != 0 {
				t.Fatal("expected READY to be requested before dispatching the job")
			}
			clock.at = clock.at.Add(time.Second)
			fleet.state = printerReady
			sc.Tick(t.Context())
			if len(fleet.printed) != 1 || fleet.printed[0].Path != "next.bgcode" {
				t.Fatal("expected the queued job after the printer confirmed READY")
			}
		})
	}
}

func TestPlateCalibrationSavesACompleteSetOrKeepsThePreviousOne(t *testing.T) {
	store, fleet, sc, clock := setup(t, "IDLE")
	settings := Settings{PlatePrinter: "p1", PlateCameraStream: "rtsp://192.168.1.10/live"}
	dir := filepath.Dir(store.file)
	p, err := openPlateCheck(dir, settings, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	sc.plate = p
	p.mayEnable = func(context.Context, string, string) bool { return true }
	level := uint8(45)
	obstruction := false
	p.snapshot = func(context.Context) (plateFrame, error) {
		img := platePhoto(level, obstruction)
		jpg := encodeTestJPEG(t, img)
		return plateFrame{jpeg: jpg, image: img, taken: clock.at, sum: sha256.Sum256(jpg)}, nil
	}
	start := func() {
		t.Helper()
		if err := p.startCalibration(t.Context(), sc, "user:operator"); err != nil {
			t.Fatal(err)
		}
	}
	tick := func() {
		clock.at = clock.at.Add(plateCalibrationGap)
		sc.Tick(t.Context())
	}
	start()
	tick()
	if p.calibration == nil || len(p.calibration.frames) != 1 || len(p.profiles) != 0 {
		t.Fatal("a duplicate camera frame should not complete the calibration")
	}
	level++
	tick()
	level++
	tick()
	if p.calibration != nil || len(p.profiles) != 1 || p.settings.EnabledBy != "" || len(fleet.printed) != 0 {
		t.Fatal("a complete calibration should save one profile and keep automatic starts disabled")
	}

	start()
	obstruction = true
	tick()
	if p.calibration == nil || p.calibration.issue != "imageChanged" || len(p.calibration.frames) != 1 {
		t.Fatal("a changed plate should be excluded while the calibration retries")
	}
	clock.at = p.calibration.expires
	sc.Tick(t.Context())
	if p.calibration != nil || p.calibrationFailure != "imageChanged" || len(p.profiles) != 1 {
		t.Fatal("the expired calibration should keep the previous set and report the rejected frame")
	}
	reloaded, err := openPlateCheck(dir, settings, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.profiles) != 1 || reloaded.matchingProfiles("IDLE", platePhoto(level, false)) == 0 ||
		reloaded.matchingProfiles("IDLE", platePhoto(level, true)) != 0 ||
		reloaded.matchingProfiles("FINISHED", platePhoto(level, false)) != 0 {
		t.Fatal("the saved set must recognize an empty plate only in its learned printer state")
	}
}

func TestPlateCalibrationToleratesOnlyUniformExposure(t *testing.T) {
	reference := platePhoto(45, false)
	exposed := platePhoto(45, false)
	for i := range exposed.Pix {
		exposed.Pix[i] += 10
	}
	if plateMatch(reference, exposed) || !plateCalibrationMatch(reference, exposed) {
		t.Fatal("only calibration should normalize a small exposure shift")
	}
	for x := 145; x < 152; x++ {
		for y := 144; y < 155; y++ {
			exposed.SetGray(x, y, color.Gray{Y: 140})
		}
	}
	if plateCalibrationMatch(reference, exposed) {
		t.Fatal("exposure normalization must not hide a local obstruction")
	}
}

func TestPlateCheckRequiresACommonCalibration(t *testing.T) {
	store, fleet, sc, clock := setup(t, "IDLE")
	commands, _ := configurePlateFixture(t, fleet, sc, clock, true)
	first, second := platePhoto(45, false), platePhoto(60, false)
	sc.plate.reference = nil
	sc.plate.profiles = []plateProfile{
		{state: "IDLE", frames: []image.Image{first}},
		{state: "IDLE", frames: []image.Image{second}},
	}
	frame := first
	sc.plate.snapshot = func(context.Context) (plateFrame, error) {
		jpg := encodeTestJPEG(t, frame)
		return plateFrame{image: frame, taken: clock.at, sum: sha256.Sum256(jpg)}, nil
	}
	_, _ = store.Add(Job{DeviceID: "p1", Path: "next.bgcode"})
	sc.Tick(t.Context())
	clock.at = clock.at.Add(plateDelay)
	frame = second
	sc.Tick(t.Context())
	if *commands != 0 || sc.plate.status != "firstMatch" || !sc.plate.first.checked.Equal(clock.at) {
		t.Fatal("matching different calibration sets must start a new pair")
	}
}

func TestPlateCheckRetriesTheSecondCapture(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		retry  time.Duration
		change func(plateFrame, plateSample) (plateFrame, error)
	}{
		{"unavailable", 40 * time.Second, func(_ plateFrame, _ plateSample) (plateFrame, error) {
			return plateFrame{}, errors.New("camera unavailable")
		}},
		{"stale", 40 * time.Second, func(frame plateFrame, _ plateSample) (plateFrame, error) {
			frame.taken = frame.taken.Add(-plateMaxAge - time.Second)
			return frame, nil
		}},
		{"repeated", 40 * time.Second, func(frame plateFrame, first plateSample) (plateFrame, error) {
			frame.sum = first.sum
			return frame, nil
		}},
		{"window expired", plateSampleWindow + time.Second, func(_ plateFrame, _ plateSample) (plateFrame, error) {
			return plateFrame{}, errors.New("camera unavailable")
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, fleet, sc, clock := setup(t, "FINISHED")
			commands, _ := configurePlateFixture(t, fleet, sc, clock, true)
			_, _ = store.Add(Job{DeviceID: "p1", Path: "next.bgcode"})
			start := clock.at
			sc.Tick(t.Context())
			first := sc.plate.first
			get := sc.plate.snapshot
			sc.plate.snapshot = func(ctx context.Context) (plateFrame, error) {
				frame, err := get(ctx)
				if err != nil {
					return plateFrame{}, err
				}
				return scenario.change(frame, first)
			}
			clock.at = start.Add(plateDelay)
			sc.Tick(t.Context())
			if *commands != 0 || sc.plate.first != first || sc.plate.status != "retryingCamera" {
				t.Fatal("the first match should be kept while waiting for a new second frame")
			}

			sc.plate.snapshot = get
			clock.at = start.Add(scenario.retry)
			sc.Tick(t.Context())
			if scenario.retry > plateSampleWindow {
				if *commands != 0 || sc.plate.status != "firstMatch" || !sc.plate.first.checked.Equal(clock.at) {
					t.Fatal("a frame after the window must start a new pair")
				}
				return
			}
			if *commands != 1 || sc.plate.status != "waitingReady" || len(fleet.printed) != 0 {
				t.Fatal("the fresh retry should request READY and wait for the printer to confirm")
			}
		})
	}
}

func TestPlateReadyCannotAuthorizeAnotherJobOrSurviveRestart(t *testing.T) {
	for _, scenario := range []string{"queue changed", "restart", "expired", "lost reply"} {
		t.Run(scenario, func(t *testing.T) {
			store, fleet, sc, clock := setup(t, "FINISHED")
			_, _ = configurePlateFixture(t, fleet, sc, clock, true)
			if scenario == "lost reply" {
				sc.command = func(context.Context, deviceapi.CommandParams) (deviceapi.Result, error) {
					return deviceapi.Result{}, context.DeadlineExceeded
				}
			}
			first, _ := store.Add(Job{DeviceID: "p1", Path: "first.bgcode"})
			_, _ = store.Add(Job{DeviceID: "p1", Path: "second.bgcode"})
			sc.Tick(t.Context())
			clock.at = clock.at.Add(plateDelay)
			sc.Tick(t.Context())
			switch scenario {
			case "queue changed":
				if err := store.Remove(first.ID); err != nil {
					t.Fatal(err)
				}
			case "restart":
				reopened, err := OpenStore(filepath.Dir(store.file), clock.now, store.keep)
				if err != nil {
					t.Fatal(err)
				}
				sc.store = reopened
				sc.plate = nil
			case "expired":
				clock.at = clock.at.Add(platePermitTTL)
			}
			fleet.state = printerReady
			sc.Tick(t.Context())
			if len(fleet.printed) != 0 {
				t.Fatal("an automatically set READY must not start a print without a valid permit")
			}
			// Observe cancellation, then accept an operator's new READY.
			fleet.state = "IDLE"
			sc.Tick(t.Context())
			fleet.state = printerReady
			sc.Tick(t.Context())
			if len(fleet.printed) != 1 {
				t.Fatal("manual READY should work again after cancellation")
			}
		})
	}
}

func configurePlateFixture(t *testing.T, fleet *fakeFleet, sc *Scheduler, clock *clock, automatic bool) (commands, samples *int) {
	t.Helper()
	ended := clock.at.Add(-time.Minute)
	fleet.recent.Last = &deviceapi.Job{ID: "previous", Ended: &ended, Outcome: event.JobFinished}
	sc.fleet = &plateFleet{fakeFleet: fleet, epoch: 1}
	commands, samples = new(int), new(int)
	sc.plate = &plateCheck{
		settings:  plateSettings{Device: "p1"},
		reference: platePhoto(45, false),
		mayEnable: func(context.Context, string, string) bool { return true },
		snapshot: func(context.Context) (plateFrame, error) {
			*samples += 1
			img := platePhoto(45+uint8(*samples%3), false)
			jpg := encodeTestJPEG(t, img)
			return plateFrame{image: img, taken: clock.at, sum: sha256.Sum256(jpg)}, nil
		},
	}
	sc.command = func(_ context.Context, p deviceapi.CommandParams) (deviceapi.Result, error) {
		if p.ID != "p1" || p.Verb != readyVerb {
			t.Fatalf("unexpected command: %+v", p)
		}
		*commands += 1
		return deviceapi.Result{OK: true}, nil
	}
	if automatic {
		sc.plate.settings.EnabledBy = "user:operator"
	}
	return commands, samples
}
