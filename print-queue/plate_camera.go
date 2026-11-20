package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"time"
)

const (
	maxPlateJPEG = 3 << 20
	plateMaxAge  = 8 * time.Second
)

type plateFrame struct {
	jpeg  []byte
	image image.Image
	taken time.Time
	sum   [32]byte
}

func decodePlateJPEG(raw []byte) (image.Image, error) {
	if len(raw) == 0 || len(raw) > maxPlateJPEG {
		return nil, errors.New("invalid camera snapshot size")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || !plateDimensions(config.Width, config.Height) {
		return nil, errors.New("camera snapshot dimensions must be between 320x240 and 2 megapixels")
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("the camera returned an invalid JPEG")
	}
	return img, nil
}

func plateDimensions(width, height int) bool {
	return width >= 320 && height >= 240 && width <= 1920 && height <= 1920 && width*height <= 1920*1080
}

// plateMatch uses the entire fixed camera view. Local blocks catch changes
// that a global average would hide. The thresholds deliberately favor holds.
func plateMatch(reference, current image.Image) bool {
	return plateMatchWithShift(reference, current, 0)
}

// A small, uniform exposure change is acceptable when collecting references.
// The local block thresholds still reject changes in shape or position.
func plateCalibrationMatch(reference, current image.Image) bool {
	if plateMatch(reference, current) {
		return true
	}
	bounds := reference.Bounds()
	if bounds != current.Bounds() {
		return false
	}
	var deltas [511]int
	var samples int
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 8 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 8 {
			r1, g1, b1 := plateRGB(reference, x, y)
			r2, g2, b2 := plateRGB(current, x, y)
			delta := (int(r2) + int(g2) + int(b2) - int(r1) - int(g1) - int(b1)) / 3
			deltas[255+delta]++
			samples++
		}
	}
	count := 0
	for i, n := range deltas {
		count += n
		if count <= samples/2 {
			continue
		}
		shift := i - 255
		return shift != 0 && shift >= -12 && shift <= 12 && plateMatchWithShift(reference, current, shift)
	}
	return false
}

func plateMatchWithShift(reference, current image.Image, shift int) bool {
	if reference.Bounds() != current.Bounds() {
		return false
	}
	bounds := reference.Bounds()
	const block = 16
	for y := bounds.Min.Y; y < bounds.Max.Y; y += block {
		for x := bounds.Min.X; x < bounds.Max.X; x += block {
			var total, changed, count int
			for py := y; py < min(y+block, bounds.Max.Y); py++ {
				for px := x; px < min(x+block, bounds.Max.X); px++ {
					r1, g1, b1 := plateRGB(reference, px, py)
					r2, g2, b2 := plateRGB(current, px, py)
					delta := max(channelDelta(r1, r2, shift), channelDelta(g1, g2, shift), channelDelta(b1, b2, shift))
					total += delta
					if delta >= 22 {
						changed++
					}
					count++
				}
			}
			if total > count*7 || changed*20 > count {
				return false
			}
		}
	}
	return true
}

func plateRGB(img image.Image, x, y int) (r, g, b uint8) {
	switch decoded := img.(type) {
	case *image.YCbCr:
		c := decoded.YCbCrAt(x, y)
		return color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
	case *image.Gray:
		y := decoded.GrayAt(x, y).Y
		return y, y, y
	default:
		r, g, b, _ := img.At(x, y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
	}
}

func channelDelta(a, b uint8, shift int) int {
	delta := int(b) - min(255, max(0, int(a)+shift))
	return max(delta, -delta)
}

func usablePlateImage(img image.Image) bool {
	var dark, bright, total int
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
			r, g, b := plateRGB(img, x, y)
			level := (int(r)*54 + int(g)*183 + int(b)*19) >> 8
			if level < 64 {
				dark++
			}
			if level > 100 {
				bright++
			}
			total++
		}
	}
	return bright*20 >= total && dark*20 >= total
}
