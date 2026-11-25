package connector

import (
	"context"
	"errors"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"io"
	"mobile-use/internal/wire"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

const mjpegFrameDuration = 125000

func (i *iosDevice) Video(parent context.Context, publish func(wire.VideoFrame) error) error {
	if i.mjpeg == "" || i.ffmpeg == "" {
		return wire.ErrUnsupported
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	check, stop := context.WithTimeout(ctx, 2*time.Second)
	err := i.requireUnlocked(check)
	stop()
	if err != nil {
		return err
	}
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				check, stop := context.WithTimeout(ctx, 2*time.Second)
				err := i.requireUnlocked(check)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return transcodeMJPEG(ctx, i.mjpeg, i.ffmpeg, publish)
}

func transcodeMJPEG(parent context.Context, location, path string, publish func(wire.VideoFrame) error) error {
	if !localHTTP(location) {
		return wire.ErrUnsupported
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 3 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, "GET", location, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "multipart/x-mixed-replace") {
		return wire.Error("IOS_VIDEO_UNAVAILABLE", "The configured WebDriverAgent MJPEG stream is unavailable.", 503)
	}
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-loglevel", "error", "-f", "mpjpeg", "-i", "pipe:0", "-an",
		"-vf", "scale=1280:1280:force_original_aspect_ratio=decrease:force_divisible_by=2", "-r", "8",
		"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-preset", "ultrafast", "-tune", "zerolatency",
		"-b:v", "1600k", "-g", "8", "-sc_threshold", "0", "-x264-params", "aud=1:repeat-headers=1", "-f", "h264", "pipe:1")
	cmd.Stdin, cmd.Stderr = response.Body, io.Discard
	cmd.WaitDelay = time.Second
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return wire.Error("VIDEO_ENCODER_UNAVAILABLE", "Install FFmpeg with libx264 on the iOS connector.", 503)
	}
	defer func() { cancel(); _ = response.Body.Close(); _ = cmd.Wait() }()
	return readH264AccessUnits(output, publish)
}

func readH264AccessUnits(input io.Reader, publish func(wire.VideoFrame) error) error {
	reader, err := h264reader.NewReader(input)
	if err != nil {
		return err
	}
	var unit []byte
	var picture bool
	flush := func() error {
		if picture {
			if err := publish(wire.VideoFrame{Data: unit, DurationMicros: mjpegFrameDuration}); err != nil {
				return err
			}
		}
		unit, picture = nil, false
		return nil
	}
	for {
		nalu, err := reader.NextNAL()
		if errors.Is(err, io.EOF) {
			return flush()
		}
		if err != nil {
			return err
		}
		kind := byte(nalu.UnitType)
		if kind == 9 && picture {
			if err := flush(); err != nil {
				return err
			}
		}
		if len(unit)+len(nalu.Data) > maxFrameBytes {
			return wire.ErrUnsupported
		}
		unit = append(unit, 0, 0, 0, 1)
		unit = append(unit, nalu.Data...)
		if kind == 1 || kind == 5 {
			picture = true
		}
	}
}
