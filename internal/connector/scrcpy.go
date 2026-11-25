package connector

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"mobile-use/internal/wire"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	scrcpyVersion = "4.0"
	scrcpySHA256  = "84924bd564a1eb6089c872c7521f968058977f91f5ff02514a8c74aff3210f3a"
	maxFrameBytes = 2 << 20
	flagSession   = uint64(1) << 63
	flagConfig    = uint64(1) << 62
	flagKey       = uint64(1) << 61
)

func VerifyScrcpy(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(file, 4<<20))
	if err != nil {
		return err
	}
	if n >= 4<<20 || hex.EncodeToString(digest.Sum(nil)) != scrcpySHA256 {
		return fmt.Errorf("scrcpy-server must match the published scrcpy %s SHA-256", scrcpyVersion)
	}
	return nil
}

func (a Android) Video(parent context.Context, found Found, publish func(wire.VideoFrame) error) error {
	if err := VerifyScrcpy(a.ScrcpyServer); err != nil {
		return wire.Error("INVALID_VIDEO_SERVER", "The configured scrcpy server failed verification.", 503)
	}
	device := &androidDevice{adb: a.path(), id: found.NativeID}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	setup, finishSetup := context.WithTimeout(ctx, 15*time.Second)
	defer finishSetup()
	if _, err := unlocked(setup, device); err != nil {
		return err
	}
	scid := "0" + wire.NewID()[:7]
	remote := "/data/local/tmp/mobile-use-video-" + scid + ".jar"
	if _, err := device.run(setup, "push", a.ScrcpyServer, remote); err != nil {
		return err
	}
	defer func() {
		cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		_, _ = device.shell(cleanup, "rm -f "+remote)
	}()
	forward, err := device.run(setup, "forward", "tcp:0", "localabstract:scrcpy_"+scid)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(forward)))
	if err != nil || port < 1 || port > 65535 {
		return wire.ErrUnsupported
	}
	defer func() {
		cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		_, _ = device.run(cleanup, "forward", "--remove", "tcp:"+strconv.Itoa(port))
	}()
	args := "CLASSPATH=" + remote + " app_process / com.genymobile.scrcpy.Server " + scrcpyVersion +
		" scid=" + scid + " tunnel_forward=true audio=false control=false cleanup=false send_device_meta=false" +
		" video_codec=h264 video_bit_rate=2200000 max_size=1280 max_fps=20 video_codec_options=profile:int=1"
	process := exec.CommandContext(ctx, a.path(), "-s", found.NativeID, "shell", args)
	process.Stdout, process.Stderr = io.Discard, io.Discard
	process.WaitDelay = time.Second
	if err := process.Start(); err != nil {
		return wire.ErrUnsupported
	}
	stopped := make(chan struct{})
	go func() { _ = process.Wait(); close(stopped) }()
	defer func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
		}
	}()
	socket, err := scrcpySocket(setup, port, stopped)
	if err != nil {
		return err
	}
	defer socket.Close()
	stop := context.AfterFunc(ctx, func() { socket.Close() })
	defer stop()
	finishSetup()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(ctx, 900*time.Millisecond)
				_, err := unlocked(check, device)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return readScrcpyVideo(socket, publish)
}

func scrcpySocket(ctx context.Context, port int, stopped <-chan struct{}) (net.Conn, error) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 300 * time.Millisecond}
	for {
		if socket, err := dialer.DialContext(ctx, "tcp", address); err == nil {
			_ = socket.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
			var dummy [1]byte
			if _, err := io.ReadFull(socket, dummy[:]); err == nil && dummy[0] == 0 {
				_ = socket.SetReadDeadline(time.Time{})
				return socket, nil
			}
			socket.Close()
		}
		select {
		case <-ctx.Done():
			return nil, wire.Error("VIDEO_START_TIMEOUT", "Android video capture did not start.", 504)
		case <-stopped:
			return nil, wire.Error("VIDEO_START_FAILED", "The Android video encoder did not start on this device.", 503)
		case <-time.After(80 * time.Millisecond):
		}
	}
}

func readScrcpyVideo(input io.Reader, publish func(wire.VideoFrame) error) error {
	var codec [4]byte
	if _, err := io.ReadFull(input, codec[:]); err != nil {
		return err
	}
	if string(codec[:]) != "h264" {
		return wire.ErrUnsupported
	}
	var config []byte
	var previous uint64
	var header [12]byte
	started := false
	for {
		if _, err := io.ReadFull(input, header[:]); err != nil {
			return err
		}
		flags := binary.BigEndian.Uint64(header[:8])
		if flags&flagSession != 0 {
			width, height := binary.BigEndian.Uint32(header[4:8]), binary.BigEndian.Uint32(header[8:])
			if width == 0 || height == 0 || width > 4096 || height > 4096 {
				return wire.ErrUnsupported
			}
			started, config, previous = false, nil, 0
			continue
		}
		size := binary.BigEndian.Uint32(header[8:])
		if size == 0 || size > maxFrameBytes {
			return wire.ErrUnsupported
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(input, data); err != nil {
			return err
		}
		if flags&flagConfig != 0 {
			if len(data) > 65536 {
				return wire.ErrUnsupported
			}
			config = data
			continue
		}
		if flags&flagKey != 0 && len(config) > 0 {
			combined := make([]byte, 0, len(config)+len(data))
			combined = append(combined, config...)
			data = append(combined, data...)
			started = true
		}
		if !started {
			continue
		}
		pts := flags & (flagKey - 1)
		duration := int64(50000)
		if previous > 0 && pts > previous && pts-previous < 250000 {
			duration = int64(pts - previous)
		}
		previous = pts
		if err := publish(wire.VideoFrame{Data: data, DurationMicros: duration}); err != nil {
			return err
		}
	}
}
