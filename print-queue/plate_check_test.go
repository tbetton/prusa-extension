package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

type plateHTTP func(*http.Request) (*http.Response, error)

func (roundTrip plateHTTP) RoundTrip(r *http.Request) (*http.Response, error) { return roundTrip(r) }

func TestPlateCameraUsesTimestampAndReadToken(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	tokenFile := filepath.Join(t.TempDir(), "plate-camera.token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	camera, err := newPlateCamera("http://127.0.0.1:8080/api/v1/cameras/test/snapshot", tokenFile, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	taken := now
	jpg := encodeTestJPEG(t, platePhoto(45, false))
	camera.client.Transport = plateHTTP(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer test-token" ||
			req.Header.Get("Cache-Control") != "no-cache" {
			t.Fatal("unexpected camera request")
		}
		header := http.Header{}
		header.Set("Last-Modified", taken.Format(http.TimeFormat))
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(jpg))}, nil
	})
	if shot, err := camera.snapshot(t.Context()); err != nil || !shot.taken.Equal(now) {
		t.Fatalf("fresh capture time=%s err=%v", shot.taken, err)
	}
	taken = now.Add(-plateMaxAge - time.Second)
	if _, err := camera.snapshot(t.Context()); err == nil {
		t.Fatal("stale photo was accepted")
	}
}

func TestPlateCameraReportsAuthenticationAndUsesConfiguredCredential(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tokenFile := filepath.Join(t.TempDir(), "plate-camera.token")
	camera, err := newPlateCamera("http://127.0.0.1:8090/api/v1/cameras/test/snapshot", tokenFile, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	jpg := encodeTestJPEG(t, platePhoto(45, false))
	camera.client.Transport = plateHTTP(func(req *http.Request) (*http.Response, error) {
		header := http.Header{}
		switch req.Header.Get("Authorization") {
		case "":
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: header, Body: http.NoBody}, nil
		case "Bearer configured-token":
			header.Set("Last-Modified", now.Format(http.TimeFormat))
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(jpg))}, nil
		default:
			t.Fatal("unexpected Authorization header")
			return nil, nil
		}
	})
	if _, err := camera.snapshot(t.Context()); err == nil || !strings.Contains(err.Error(), "plate_camera_token") {
		t.Fatalf("wanted the token setting in the HTTP 401 error, got %v", err)
	}
	if err := os.WriteFile(tokenFile, []byte("legacy-file-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	camera.token = "configured-token"
	if shot, err := camera.snapshot(t.Context()); err != nil || !shot.taken.Equal(now) {
		t.Fatalf("configured token should override the token file, got photo %s and error %v", shot.taken, err)
	}
}
