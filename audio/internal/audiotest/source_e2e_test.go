//go:build e2e

package audiotest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

// The e2e build tag explicitly enables this software fixture, never hardware.
func TestE2EReferenceServerFakeInputScenario(t *testing.T) {
	var factories atomic.Int64
	receipt := runReferenceServerInputScenario(t, SourceScenarioConfig{Suite: suite, InputFactory: func(device string) audio.Input {
		factories.Add(1)
		if device != "synthetic-input-not-hardware" {
			return nil
		}
		return sourceFakeInput{}
	}}, true)
	if factories.Load() != 2 {
		t.Fatal("explicit source starts did not bind the same fake device twice")
	}
	if receipt.Status != "pass" || !receipt.Virtual || receipt.CaptureOpened || receipt.OutputOpened || receipt.SoftwareFixture || receipt.DecodedFrames <= 0 || receipt.PhysicalConformance {
		t.Fatal("fake fixture mislabeled as native/physical evidence")
	}
}

type sourceFakeInput struct{}
type sourceFakeDevice struct{}

func (sourceFakeInput) Open(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if f != sourceCaptureFormat {
		return nil, errors.New("unexpected fake source format")
	}
	return sourceFakeDevice{}, nil
}
func (sourceFakeDevice) Read(ctx context.Context, dst []float32) (int, time.Time, error) {
	duration := time.Duration(len(dst)/2) * time.Second / 48000
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, time.Time{}, ctx.Err()
	case <-timer.C:
	}
	clear(dst)
	return len(dst), time.Now().Add(-duration), nil
}
func (sourceFakeDevice) Close() error { return nil }
