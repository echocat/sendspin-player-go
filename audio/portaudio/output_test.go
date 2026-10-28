//go:build cgo

package portaudio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	pa "github.com/gordonklaus/portaudio"
)

func TestCallbackRingWrapAndUnderflow(t *testing.T) {
	d := &device{format: audio.Format{SampleRate: 48000, Channels: 1}, ring: make([]float32, 8), notify: make(chan struct{}, 1)}
	if err := d.Write(context.Background(), []float32{1, 2, 3, 4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	out := make([]float32, 4)
	d.callback(out, pa.StreamCallbackTimeInfo{OutputBufferDacTime: time.Second})
	if out[0] != 1 || out[3] != 4 {
		t.Fatal(out)
	}
	if err := d.Write(context.Background(), []float32{7, 8, 9, 10}); err != nil {
		t.Fatal(err)
	}
	out = make([]float32, 8)
	d.callback(out, pa.StreamCallbackTimeInfo{OutputBufferDacTime: time.Second})
	for i, want := range []float32{5, 6, 7, 8, 9, 10, 0, 0} {
		if out[i] != want {
			t.Fatal(out)
		}
	}
	if d.size != 0 || !d.timed {
		t.Fatal("callback did not advance queue/timing")
	}
}
func TestCancelledWriteAndInvalidOpen(t *testing.T) {
	d := &device{format: audio.Format{SampleRate: 48000, Channels: 1}, ring: make([]float32, 4), notify: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Write(ctx, []float32{1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (Output{}).Open(context.Background(), audio.Format{}); err == nil {
		t.Fatal("opened invalid format")
	}
	wait, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := d.Next(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("invented hardware timestamp")
	}
}

func TestTimingReadinessWaitsForCallbackAndPreservesCancellation(t *testing.T) {
	d := &device{format: audio.Format{SampleRate: 48000, Channels: 1}, ring: make([]float32, 8), notify: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.awaitTiming(ctx) }()
	d.callback(make([]float32, 4), pa.StreamCallbackTimeInfo{OutputBufferDacTime: time.Second})
	if err := <-done; err != nil {
		t.Fatal("first native callback did not release timing wait", err)
	}
	d.mu.Lock()
	d.timed = false
	d.mu.Unlock()
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := d.awaitTiming(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("timing wait ignored cancellation", err)
	}
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
	if err := d.awaitTiming(ctx); err == nil {
		t.Fatal("closed device supplied timing")
	}
}

func TestTimingStartupBudgetRejectsMissingNativeFeedback(t *testing.T) {
	d := &device{format: audio.Format{SampleRate: 48000, Channels: 1}, ring: make([]float32, 8), notify: make(chan struct{}, 1), timingWait: 10 * time.Millisecond}
	if err := d.awaitTiming(context.Background()); err == nil || d.timed {
		t.Fatal("missing native feedback became a timestamp")
	}
}
