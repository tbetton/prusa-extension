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
	case "", "80", "443", "8080":
	default:
		return nil, errors.New("plate_camera_url must use port 80, 443 or 8080")
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

func (c *plateCamera) snapshot(ctx context.Context) (plateFrame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return plateFrame{}, err
	}
	req.Header.Set("Accept", "image/jpeg")
	req.Header.Set("Cache-Control", "no-cache")
	if file, err := os.Open(c.tokenFile); err == nil {
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			_ = file.Close()
			return plateFrame{}, errors.New("camera token file must be private")
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, 8193))
		_ = file.Close()
		token := strings.TrimSpace(string(raw))
		if readErr != nil || len(raw) > 8192 || token == "" || strings.ContainsAny(token, " \t\r\n") {
			return plateFrame{}, errors.New("invalid camera token file")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	} else if !errors.Is(err, os.ErrNotExist) {
		return plateFrame{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return plateFrame{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return plateFrame{}, errors.New("camera has no accessible snapshot")
	}
	taken, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if now := c.now(); err != nil || taken.After(now) || now.Sub(taken) > plateMaxAge {
		return plateFrame{}, errors.New("camera snapshot is stale or has no valid timestamp")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPlateJPEG+1))
	if err != nil {
		return plateFrame{}, err
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
	return jpeg.Decode(bytes.NewReader(raw))
}

// plateMatch uses the entire fixed camera view. Local blocks catch changes
// that a global average would hide. The thresholds deliberately favor holds.
func plateMatch(reference, current image.Image) bool {
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
					delta := max(channelDelta(r1, r2), channelDelta(g1, g2), channelDelta(b1, b2))
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

func channelDelta(a, b uint8) int {
	return int(max(a, b) - min(a, b))
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
