package noop

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestNoopPacingCancellationFlushAndClose(t *testing.T) {
	ctx := context.Background()
	d, err := (Output{}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, s := range [][]float32{{0}, {float32(math.NaN()), 0}, {2, 0}} {
		if err = d.Write(ctx, s); err == nil {
			t.Fatal("invalid samples accepted")
		}
	}
	done := make(chan error, 1)
	go func() { done <- d.Write(ctx, make([]float32, 48000*2)) }()
	deadline := time.Now().Add(time.Second)
	for {
		state := d.(*device)
		state.mu.Lock()
		queued := !state.tail.IsZero()
		state.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("discard write did not enter pacing")
		}
		time.Sleep(time.Millisecond)
	}
	if err = d.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Flush did not unblock discard")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = d.Write(canceled, []float32{0, 0}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Next(ctx); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err = (Output{}).Open(ctx, audio.Format{}); err == nil {
		t.Fatal("invalid format accepted")
	}
}
