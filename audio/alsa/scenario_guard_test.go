package alsa

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestScenarioGuardRejectsUnexpectedFramesBeforeForwarding(t *testing.T) {
	for _, samples := range [][]float32{{.02, 0}, {0, .001}, {float32(math.NaN()), 0}, {0}} {
		sink := &referenceSink{}
		owner := &scenarioOutput{}
		d := &scenarioDevice{Device: sink, owner: owner}
		if err := d.Write(context.Background(), samples); err == nil || sink.frames != 0 || owner.failures.Load() != 1 {
			t.Fatal("unsafe output reached sink", err, sink.frames)
		}
	}
	sink := &referenceSink{}
	owner := &scenarioOutput{}
	d := &scenarioDevice{Device: sink, owner: owner}
	if err := d.Write(context.Background(), []float32{.01, 0}); err != nil || sink.frames != 1 || owner.signalWrites.Load() != 1 {
		t.Fatal("approved weak frame not forwarded", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil || owner.closes.Load() != 1 {
		t.Fatal("close not idempotent", err)
	}
	if _, err := owner.Open(context.Background(), audio.Format{SampleRate: 44100, Channels: 2}); err == nil {
		t.Fatal("unexpected native format accepted")
	}
	if !errors.Is(owner.record(context.Canceled), context.Canceled) {
		t.Fatal("cancellation lost")
	}
}
