package playback

import (
	"context"
	"errors"
	"github.com/echocat/sendspin-player-go/audio"
	"testing"
	"time"
)

type resetSignal struct{ flush bool }

func (resetSignal) Error() string              { return "test native reset" }
func (r resetSignal) AudioTimelineReset() bool { return r.flush }

type resetDevice struct {
	at                              time.Time
	next, writes, flushes, failures int
	flush                           bool
	received                        []float32
}

func (d *resetDevice) Next(context.Context) (time.Time, error) {
	d.next++
	if d.next > 1 {
		return d.at.Add(5 * time.Millisecond), nil
	}
	return d.at, nil
}
func (d *resetDevice) Write(_ context.Context, s []float32) error {
	d.writes++
	if d.writes <= d.failures {
		return resetSignal{d.flush}
	}
	d.received = append(d.received, s...)
	return nil
}
func (d *resetDevice) Flush(context.Context) error { d.flushes++; return nil }
func (d *resetDevice) Close() error                { return nil }
func TestNativeTimelineRecoveryRemapsWithoutReplayingStaleSamples(t *testing.T) {
	for _, flush := range []bool{false, true} {
		at := time.Now()
		d := &resetDevice{at: at, failures: 1, flush: flush}
		e := New(nil, 10000, func(int64) (time.Time, error) { return at, nil })
		var gain float32
		samples := make([]float32, 480)
		for i := range samples {
			samples[i] = 1
		}
		if err := e.renderRecovering(context.Background(), d, Chunk{Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: samples}, &gain); err != nil {
			t.Fatal(err)
		}
		wantFlush := 0
		if flush {
			wantFlush = 1
		}
		if d.flushes != wantFlush || len(d.received) != 240 || d.received[0] >= 1 {
			t.Fatal("reset did not remap/drop/ramp correctly", flush, d.flushes, len(d.received))
		}
	}
}
func TestNativeTimelineRecoveryIsBounded(t *testing.T) {
	at := time.Now()
	d := &resetDevice{at: at, failures: 99}
	e := New(nil, 10000, func(int64) (time.Time, error) { return at, nil })
	var gain float32
	err := e.renderRecovering(context.Background(), d, Chunk{Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: make([]float32, 480)}, &gain)
	var reset resetSignal
	if !errors.As(err, &reset) || d.writes != 4 {
		t.Fatal("reset loop unbounded or failure swallowed", err, d.writes)
	}
}
