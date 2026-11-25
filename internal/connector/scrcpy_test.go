package connector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"mobile-use/internal/wire"
	"os"
	"strings"
	"testing"
	"time"
)

func TestScrcpyPacketBoundaries(t *testing.T) {
	var input bytes.Buffer
	input.WriteString("h264")
	var header [12]byte
	binary.BigEndian.PutUint32(header[:4], 0x80000000)
	binary.BigEndian.PutUint32(header[4:8], 720)
	binary.BigEndian.PutUint32(header[8:], 1280)
	input.Write(header[:])
	appendPacket := func(flags uint64, data []byte) {
		binary.BigEndian.PutUint64(header[:8], flags)
		binary.BigEndian.PutUint32(header[8:], uint32(len(data)))
		input.Write(header[:])
		input.Write(data)
	}
	appendPacket(flagConfig, []byte{0, 0, 0, 1, 0x67})
	appendPacket(flagKey|50000, []byte{0, 0, 0, 1, 0x65})
	appendPacket(105000, []byte{0, 0, 0, 1, 0x61})
	stop := errors.New("two packets received")
	var frames []wire.VideoFrame
	err := readScrcpyVideo(&input, func(frame wire.VideoFrame) error {
		frames = append(frames, frame)
		if len(frames) == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || len(frames) != 2 || len(frames[0].Data) != 10 || frames[1].DurationMicros != 55000 {
		t.Fatalf("incorrect capture packet decoding: %v, %+v", err, frames)
	}
}

func TestScrcpyEmulator(t *testing.T) {
	path, serial := os.Getenv("MOBILE_USE_TEST_SCRCPY"), os.Getenv("MOBILE_USE_TEST_EMULATOR")
	if path == "" || !strings.HasPrefix(serial, "emulator-") {
		t.Skip("provide the scrcpy server and a disposable emulator")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	a := Android{ScrcpyServer: path}
	stop := errors.New("captured video frames")
	count := 0
	err := a.Video(ctx, Found{NativeID: serial}, func(frame wire.VideoFrame) error {
		if len(frame.Data) < 5 || frame.DurationMicros < 1 {
			t.Fatal("invalid Android video frame")
		}
		count++
		if count >= 4 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("scrcpy emulator capture did not produce four frames: %v (frames %d)", err, count)
	}
}
