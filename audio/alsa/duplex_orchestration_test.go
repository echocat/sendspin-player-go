package alsa

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

type fakeDuplexOutput struct {
	failure     error
	nextFailure error
	closed      int
	writes      atomic.Int64
}

func (d *fakeDuplexOutput) Next(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Now(), d.nextFailure
}
func (d *fakeDuplexOutput) Write(ctx context.Context, s []float32) error {
	for _, v := range s {
		if v != 0 {
			return errors.New("nonzero sample in silence fixture")
		}
	}
	if d.failure != nil {
		return d.failure
	}
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		d.writes.Add(1)
		return nil
	}
}
func (d *fakeDuplexOutput) Flush(context.Context) error { return nil }
func (d *fakeDuplexOutput) Close() error                { d.closed++; return nil }

type fakeDuplexRecorder struct {
	startFailure, waitFailure error
	duration                  time.Duration
	stopped                   chan struct{}
	once                      sync.Once
	started, waited           bool
}

func (r *fakeDuplexRecorder) Start() error {
	if r.startFailure != nil {
		return r.startFailure
	}
	r.started = true
	return nil
}
func (r *fakeDuplexRecorder) Wait() error {
	defer func() { r.waited = true }()
	timer := time.NewTimer(r.duration)
	defer timer.Stop()
	select {
	case <-r.stopped:
		return context.Canceled
	case <-timer.C:
		return r.waitFailure
	}
}
func (r *fakeDuplexRecorder) Stop() { r.once.Do(func() { close(r.stopped) }) }

func TestDuplexOrchestrationIsZeroOnlyAndJoinsBothWorkers(t *testing.T) {
	d := &fakeDuplexOutput{}
	r := &fakeDuplexRecorder{stopped: make(chan struct{}), duration: 20 * time.Millisecond}
	open := func(context.Context, audio.Format) (audio.Device, error) { return d, nil }
	frames, _, err := duplexLoop(context.Background(), open, r)
	if err != nil || frames <= 0 || d.closed != 1 || !r.started || !r.waited {
		t.Fatal("workers not completed/joined", frames, d.closed, err)
	}
}
func TestDuplexFailuresStopRecorderAndCloseOutput(t *testing.T) {
	fault := errors.New("controlled duplex failure")
	for _, mode := range []string{"open", "next", "start", "write", "record", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			d := &fakeDuplexOutput{}
			r := &fakeDuplexRecorder{stopped: make(chan struct{}), duration: time.Hour}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			open := func(context.Context, audio.Format) (audio.Device, error) {
				if mode == "open" {
					return nil, fault
				}
				return d, nil
			}
			switch mode {
			case "next":
				d.nextFailure = fault
			case "start":
				r.startFailure = fault
			case "write":
				d.failure = fault
			case "record":
				r.duration = time.Millisecond
				r.waitFailure = fault
			}
			_, _, err := duplexLoop(ctx, open, r)
			if mode == "cancel" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, fault) {
				t.Fatal("primary failure lost", err)
			}
			if mode != "open" && d.closed != 1 {
				t.Fatal("output leaked")
			}
			if r.started && !r.waited {
				t.Fatal("recorder not joined")
			}
			if mode == "open" || mode == "next" {
				if r.started {
					t.Fatal("recorder opened before output readiness")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, _, err := duplexLoop(ctx, func(context.Context, audio.Format) (audio.Device, error) { called = true; return nil, nil }, nil)
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled operation opened a device")
	}
}

type zeroFixtureReader struct{}

func (zeroFixtureReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestDuplexArtifactsAreExclusiveAndContainerChecksReadNoSamples(t *testing.T) {
	root := t.TempDir()
	dir, capture, diagnostics, err := createDuplexArtifacts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capture.Close(); _ = diagnostics.Close() }()
	if runtime.GOOS != "windows" {
		for _, p := range []string{dir, filepath.Join(dir, "capture.wav"), filepath.Join(dir, "arecord.log")} {
			stat, err := os.Stat(p)
			if err != nil || stat.Mode().Perm()&0077 != 0 {
				t.Fatal("private capture permissions", err)
			}
		}
	}
	const dataBytes = 480000 * 6
	var h [44]byte
	copy(h[:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], dataBytes+36)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 2)
	binary.LittleEndian.PutUint32(h[24:], 48000)
	binary.LittleEndian.PutUint32(h[28:], 288000)
	binary.LittleEndian.PutUint16(h[32:], 6)
	binary.LittleEndian.PutUint16(h[34:], 24)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataBytes)
	if _, err = capture.Write(h[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = io.CopyN(capture, zeroFixtureReader{}, dataBytes); err != nil {
		t.Fatal(err)
	}
	frames, bytes, err := captureGeometry(capture)
	if err != nil || frames != 480000 || bytes != dataBytes+44 {
		t.Fatal("synthetic WAV geometry", frames, bytes, err)
	}
	if err = capture.Truncate(44); err != nil {
		t.Fatal(err)
	}
	if _, _, err = captureGeometry(capture); err == nil {
		t.Fatal("incomplete capture accepted")
	}
	if err = writeDuplexReport(dir, duplexResult{Schema: 1, Status: "synthetic-only"}); err != nil {
		t.Fatal(err)
	}
	if err = writeDuplexReport(dir, duplexResult{Schema: 1}); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing report overwritten", err)
	}
	if _, _, _, err = createDuplexArtifacts("relative"); err == nil {
		t.Fatal("ambiguous artifact root accepted")
	}
}
