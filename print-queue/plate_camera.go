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
	"net/url"
	"os"
	"path"
	"strings"
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

type plateCamera struct {
	url       string
	token     string
	tokenFile string
	client    *http.Client
	now       func() time.Time
}

func newPlateCamera(rawURL, tokenFile string, now func() time.Time) (*plateCamera, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawPath != "" || (u.Scheme != "http" && u.Scheme != "https") ||
		path.Clean(u.Path) != u.Path || !strings.HasPrefix(u.Path, "/api/v1/cameras/") ||
		!strings.HasSuffix(u.Path, "/snapshot") || strings.Count(u.Path, "/") != 5 {
		return nil, errors.New("plate_camera_url must be the Prusa Connect camera snapshot URL")
	}
	switch u.Port() {
	case "", "80", "443", "8080", "8090":
	default:
		return nil, errors.New("plate_camera_url must use port 80, 443, 8080 or 8090")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &plateCamera{
		url: u.String(), tokenFile: tokenFile, now: now,
		client: &http.Client{
			Transport: transport, Timeout: 3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *plateCamera) readToken() (string, error) {
	token := strings.TrimSpace(c.token)
	if token == "" {
		file, err := os.Open(c.tokenFile)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", errors.New("the camera token file could not be read")
		}
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("the camera token file must be private")
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, 8193))
		if readErr != nil || len(raw) > 8192 {
			return "", errors.New("the camera token file could not be read")
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" || len(token) > 8192 || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("enter a valid camera API token in the Print Queue settings")
	}
	return token, nil
}

func (c *plateCamera) snapshot(ctx context.Context) (plateFrame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return plateFrame{}, errors.New("the configured camera snapshot URL is invalid")
	}
	req.Header.Set("Accept", "image/jpeg")
	req.Header.Set("Cache-Control", "no-cache")
	token, err := c.readToken()
	if err != nil {
		return plateFrame{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return plateFrame{}, errors.New("Print Queue could not connect to the configured Prusa Connect server")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return plateFrame{}, errors.New("Prusa Connect requires a valid read-only API token for the camera. Set plate_camera_token in Print Queue settings")
	case http.StatusForbidden, http.StatusNotFound:
		return plateFrame{}, errors.New("Prusa Connect could not find the camera or the API token cannot observe it")
	case http.StatusNoContent:
		return plateFrame{}, errors.New("Prusa Connect has no image from this camera yet")
	default:
		return plateFrame{}, errors.New("Prusa Connect could not serve the camera snapshot")
	}
	taken, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if now := c.now(); err != nil || taken.After(now) || now.Sub(taken) > plateMaxAge {
		return plateFrame{}, errors.New("camera snapshot is stale or has no valid timestamp")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPlateJPEG+1))
	if err != nil {
		return plateFrame{}, errors.New("the camera snapshot could not be downloaded")
	}
	img, err := decodePlateJPEG(raw)
	if err != nil {
		return plateFrame{}, err
	}
	return plateFrame{jpeg: raw, image: img, taken: taken, sum: sha256.Sum256(raw)}, nil
}

func decodePlateJPEG(raw []byte) (image.Image, error) {
	if len(raw) == 0 || len(raw) > maxPlateJPEG {
		return nil, errors.New("invalid camera snapshot size")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width < 320 || config.Height < 240 ||
		config.Width > 1920 || config.Height > 1920 || config.Width*config.Height > 1920*1080 {
		return nil, errors.New("camera snapshot dimensions must be between 320x240 and 2 megapixels")
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("the camera returned an invalid JPEG")
	}
	return img, nil
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
