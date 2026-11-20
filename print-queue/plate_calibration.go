package main

import (
	"context"
	"errors"
	"image"
	"slices"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

const (
	plateCalibrationFrames = 3
	plateCalibrationGap    = 10 * time.Second
	plateCalibrationLimit  = 90 * time.Second
	plateProfilesPerState  = 2
)

type savedPlateProfile struct {
	State  string    `json:"state"`
	At     time.Time `json:"at"`
	Frames [][]byte  `json:"frames"`
}

type plateProfile struct {
	state  string
	frames []image.Image
}

type plateCalibrationContext struct {
	state    string
	previous string
	ended    time.Time
	epoch    uint64
}

type plateCalibration struct {
	context  plateCalibrationContext
	operator string
	expires  time.Time
	frames   []plateFrame
}

type plateCalibrationView struct {
	Captured int  `json:"captured"`
	Target   int  `json:"target"`
	Failed   bool `json:"failed,omitempty"`
}

func decodePlateProfiles(saved []savedPlateProfile) ([]plateProfile, error) {
	var profiles []plateProfile
	counts := map[string]int{}
	for _, entry := range saved {
		counts[entry.State]++
		if (entry.State != "IDLE" && entry.State != "FINISHED") || entry.At.IsZero() ||
			counts[entry.State] > plateProfilesPerState || len(entry.Frames) != plateCalibrationFrames {
			return nil, errors.New("invalid saved plate calibration")
		}
		profile := plateProfile{state: entry.State}
		for _, raw := range entry.Frames {
			frame, err := decodePlateJPEG(raw)
			if err != nil || !usablePlateImage(frame) {
				return nil, errors.New("invalid image in saved plate calibration")
			}
			profile.frames = append(profile.frames, frame)
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (p *plateCheck) hasReference() bool { return p.reference != nil || len(p.profiles) > 0 }

// Both checks must agree with a common calibration for this printer state.
func (p *plateCheck) matchingProfiles(state string, frame image.Image) uint16 {
	var matches uint16
	if !usablePlateImage(frame) {
		return matches
	}
	if p.reference != nil && plateMatch(p.reference, frame) {
		matches = 1
	}
	for i, profile := range p.profiles {
		if profile.state == state && slices.ContainsFunc(profile.frames, func(ref image.Image) bool {
			return plateMatch(ref, frame)
		}) {
			matches |= 1 << (i + 1)
		}
	}
	return matches
}

func (p *plateCheck) startCalibration(ctx context.Context, sc *Scheduler, operator string) error {
	first, capture, err := p.referenceFrame(ctx, sc, false)
	if err != nil {
		return err
	}
	next := p.settings
	next.EnabledBy = ""
	if err := p.save(next); err != nil {
		return StoreFailed.New("the plate settings could not be saved")
	}
	p.permit = platePermit{}
	p.lastMatch = nil
	p.reset("waiting")
	p.calibrationFailed = false
	p.calibration = &plateCalibration{
		context: capture, operator: operator, expires: sc.now().Add(plateCalibrationLimit),
		frames: []plateFrame{first},
	}
	return nil
}

func (p *plateCheck) updateCalibration(ctx context.Context, sc *Scheduler, device deviceapi.Device) {
	calibration := p.calibration
	now := sc.now()
	if !now.Before(calibration.expires) || !platePrinterAvailable(device) ||
		printerState(device) != calibration.context.state || device.CapabilityEpoch != calibration.context.epoch ||
		!p.mayEnable(ctx, calibration.operator, p.settings.Device) {
		p.calibration, p.calibrationFailed = nil, true
		return
	}
	last := calibration.frames[len(calibration.frames)-1]
	if now.Sub(last.taken) < plateCalibrationGap {
		return
	}
	frame, capture, err := p.referenceFrame(ctx, sc, false)
	if err != nil {
		return
	}
	if capture != calibration.context || !plateMatch(calibration.frames[0].image, frame.image) ||
		!sc.now().Before(calibration.expires) {
		p.calibration, p.calibrationFailed = nil, true
		return
	}
	if frame.taken.Sub(last.taken) < plateCalibrationGap || slices.ContainsFunc(calibration.frames, func(previous plateFrame) bool {
		return previous.sum == frame.sum
	}) {
		return
	}
	calibration.frames = append(calibration.frames, frame)
	if len(calibration.frames) != plateCalibrationFrames {
		return
	}

	saved := savedPlateProfile{State: capture.state, At: frame.taken}
	profile := plateProfile{state: capture.state}
	for _, shot := range calibration.frames {
		saved.Frames = append(saved.Frames, shot.jpeg)
		profile.frames = append(profile.frames, shot.image)
	}
	next := p.settings
	next.Profiles = slices.Clone(next.Profiles)
	profiles := slices.Clone(p.profiles)
	count, oldest := 0, -1
	for i, entry := range next.Profiles {
		if entry.State == capture.state {
			count++
			if oldest == -1 {
				oldest = i
			}
		}
	}
	if count >= plateProfilesPerState {
		next.Profiles = slices.Delete(next.Profiles, oldest, oldest+1)
		profiles = slices.Delete(profiles, oldest, oldest+1)
	}
	next.Profiles = append(next.Profiles, saved)
	p.calibration = nil
	if err := p.save(next); err != nil {
		p.calibrationFailed = true
		sc.logf("save plate calibration: %v", err)
		return
	}
	p.profiles = append(profiles, profile)
}
