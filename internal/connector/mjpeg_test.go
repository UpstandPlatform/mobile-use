package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"mobile-use/internal/wire"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestMJPEGEncoder(t *testing.T) {
	if os.Getenv("MOBILE_USE_TEST_FFMPEG") != "1" {
		t.Skip("enable the real FFmpeg test with MOBILE_USE_TEST_FFMPEG=1")
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	var sample bytes.Buffer
	if err := jpeg.Encode(&sample, image.NewRGBA(image.Rect(0, 0, 160, 240)), nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		for n := 0; n < 35; n++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			_, _ = fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", sample.Len())
			_, _ = w.Write(sample.Bytes())
			_, _ = w.Write([]byte("\r\n"))
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	stop := errors.New("enough encoded frames")
	frames := 0
	err = transcodeMJPEG(ctx, server.URL, path, func(frame wire.VideoFrame) error {
		if !bytes.HasPrefix(frame.Data, []byte{0, 0, 0, 1}) || frame.DurationMicros != mjpegFrameDuration {
			t.Fatal("invalid encoded access unit")
		}
		frames++
		if frames == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("encode MJPEG: %v, frames %d", err, frames)
	}
}
