package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/jpeg"
	"net"
	"net/netip"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	go264 "github.com/oops1/go.264"
	"github.com/pion/rtp"
)

const plateRTSPTimeout = 8 * time.Second

func plateRTSPAddress(raw string) (*base.URL, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "rtsp" || parsed.User != nil || parsed.Fragment != "" {
		return nil, "", errors.New("the plate camera needs an RTSP address without embedded credentials")
	}
	ip, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || !ip.IsPrivate() || ip.Zone() != "" {
		return nil, "", errors.New("the plate camera needs a private LAN IP address")
	}
	port := parsed.Port()
	if port == "" {
		port = "554"
	}
	if port != "554" && port != "8554" {
		return nil, "", errors.New("the plate camera needs RTSP port 554 or 8554")
	}
	address, err := base.ParseURL(parsed.String())
	return address, net.JoinHostPort(parsed.Hostname(), port), err
}

type plateRTSPKeyframe struct {
	data  []byte
	taken time.Time
}

func readPlateRTSP(ctx context.Context, address *base.URL, peer string, now func() time.Time) (plateFrame, error) {
	ctx, cancel := context.WithTimeout(ctx, plateRTSPTimeout)
	defer cancel()
	tcp := gortsplib.ProtocolTCP
	client := gortsplib.Client{
		Scheme: address.Scheme, Host: address.Host, Protocol: &tcp,
		ReadTimeout: plateRTSPTimeout, WriteTimeout: 3 * time.Second,
		DialContext: func(dialCtx context.Context, network, target string) (net.Conn, error) {
			if target != peer {
				return nil, errors.New("the RTSP camera redirected to another address")
			}
			return (&net.Dialer{}).DialContext(dialCtx, network, target)
		},
	}
	if err := client.Start(); err != nil {
		return plateFrame{}, errors.New("could not start the plate camera RTSP client")
	}
	stop := context.AfterFunc(ctx, client.Close)
	defer func() {
		if stop() {
			client.Close()
		}
	}()
	desc, _, err := client.Describe(address)
	if err != nil {
		return plateFrame{}, errors.New("could not read the plate camera RTSP stream")
	}
	var video *format.H264
	media := desc.FindFormat(&video)
	if media == nil {
		return plateFrame{}, errors.New("the RTSP plate camera must provide H.264 video")
	}
	depacketizer, err := video.CreateDecoder()
	if err != nil {
		return plateFrame{}, errors.New("could not read the RTSP camera video packets")
	}
	if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
		return plateFrame{}, errors.New("could not set up the RTSP camera video")
	}

	keyframes := make(chan plateRTSPKeyframe, 1)
	sps, pps := video.SPS, video.PPS
	client.OnPacketRTP(media, video, func(packet *rtp.Packet) {
		if len(keyframes) != 0 || ctx.Err() != nil {
			return
		}
		units, err := depacketizer.Decode(packet)
		if err != nil {
			return
		}
		var idr bool
		for _, unit := range units {
			if len(unit) == 0 || len(unit) > maxPlateJPEG {
				continue
			}
			switch unit[0] & 31 {
			case 5:
				idr = true
			case 7:
				sps = bytes.Clone(unit)
			case 8:
				pps = bytes.Clone(unit)
			}
		}
		if !idr || len(sps) == 0 || len(pps) == 0 {
			return
		}
		data := []byte{}
		for _, unit := range append([][]byte{sps, pps}, units...) {
			if len(unit) == 0 || len(data)+4+len(unit) > maxPlateJPEG {
				return
			}
			data = append(data, 0, 0, 0, 1)
			data = append(data, unit...)
		}
		keyframes <- plateRTSPKeyframe{data: data, taken: now()}
	})
	if _, err := client.Play(nil); err != nil {
		return plateFrame{}, errors.New("could not play the RTSP camera video")
	}
	select {
	case keyframe := <-keyframes:
		frame, err := decodePlateRTSP(keyframe)
		if ctx.Err() != nil {
			return plateFrame{}, errors.New("RTSP camera capture timed out")
		}
		return frame, err
	case <-ctx.Done():
		return plateFrame{}, errors.New("the RTSP camera did not provide a fresh keyframe")
	}
}

func decodePlateRTSP(keyframe plateRTSPKeyframe) (plateFrame, error) {
	decoder := go264.NewDecoderWithConfig(go264.DecoderConfig{
		ForceSoftware: true,
		Limits:        go264.DecoderLimits{MaxFrameMBs: 8160, MaxNALBytes: maxPlateJPEG},
	})
	defer decoder.Close()
	pictures, err := decoder.Decode(keyframe.data)
	if err == nil && len(pictures) == 0 {
		pictures, err = decoder.Flush()
	}
	if err != nil || len(pictures) == 0 {
		return plateFrame{}, errors.New("could not decode the RTSP camera keyframe")
	}
	picture := pictures[0]
	if !plateDimensions(picture.Width, picture.Height) {
		return plateFrame{}, errors.New("the RTSP camera image must be between 320x240 and 2 megapixels")
	}
	frame := &image.YCbCr{
		Y: picture.Y, Cb: picture.Cb, Cr: picture.Cr,
		YStride: picture.StrideY, CStride: picture.StrideC,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, picture.Width, picture.Height),
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, frame, &jpeg.Options{Quality: 88}); err != nil || jpg.Len() > maxPlateJPEG {
		return plateFrame{}, errors.New("could not encode the RTSP camera image as JPEG")
	}
	raw := jpg.Bytes()
	img, err := decodePlateJPEG(raw)
	if err != nil {
		return plateFrame{}, err
	}
	return plateFrame{jpeg: raw, image: img, taken: keyframe.taken, sum: sha256.Sum256(raw)}, nil
}
