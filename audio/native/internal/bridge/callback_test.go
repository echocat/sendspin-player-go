//go:build cgo && (linux || windows || darwin)

package bridge

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/native/internal/render"
	"github.com/echocat/slf4g/testing/recording"
)

type probeDriver struct{ token uintptr }

func (d *probeDriver) Start() error { probe(d.token, 96, Now()+int64(10*time.Millisecond)); return nil }
func (d *probeDriver) Stop() error  { return nil }
func (d *probeDriver) Close() error { return nil }
func (d *probeDriver) Check() error { return nil }
func (d *probeDriver) Clock() int64 { return Now() }
func TestNativeCallbackABIAndClockWithoutAudio(t *testing.T) {
	if a, b := Now(), Now(); a <= 0 || b < a {
		t.Fatal("invalid native monotonic clock", a, b)
	}
	driver := &probeDriver{}
	d, err := render.Open(context.Background(), render.Config{Backend: "ABI probe", MaxTimingWindow: 50 * time.Millisecond, Logger: recording.NewLogger()}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *render.Device) (render.Driver, error) { driver.token = New(d); return driver, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer Free(driver.token)
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err = d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	samples := []float32{.5, -.5, .25, -.25}
	if err = d.Write(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	clear(samples)
	out := probe(driver.token, 96, Now()+int64(10*time.Millisecond))
	if !slices.Equal(out[:4], []float32{.5, -.5, .25, -.25}) || !slices.Equal(out[4:], make([]float32, 92)) {
		t.Fatal("C/Go callback boundary corrupted audio", out[:4])
	}
	probeFailure(driver.token)
	if _, err = d.Next(context.Background()); !errors.Is(err, render.ErrDriver) {
		t.Fatal("native failure callback lost", err)
	}
}

func TestNativeTimingTransformsAndRejectedDomains(t *testing.T) {
	// PipeWire graph ticks (48kHz), stream frames (44.1kHz), and absolute ns
	// are deliberately different domains. Negative graph offsets clamp to zero.
	if got := probePW(1e9, 480, 1, 48000, 441, 441, 44100); got != 1030000000 {
		t.Fatal(got)
	}
	if got := probePW(1e9, -480, 1, 48000, 0, 0, 44100); got != 1e9 {
		t.Fatal(got)
	}
	if got := probeWA(4800, 48000, 240000, 4800000, 10000000, 239999); got != 1050000000 {
		t.Fatal(got)
	}
	if got := probeCA(1e9, 480, 48000); got != 1010000000 {
		t.Fatal(got)
	}
	for _, value := range []int64{
		probePW(0, 480, 1, 48000, 0, 0, 48000), probePW(1e9, 480, 0, 48000, 0, 0, 48000), probePW(1e9, 480, 1, 0, 0, 0, 48000), probePW(1e9, 0, 1, 48000, math.MaxUint64, 0, 48000),
		probePW(math.MaxInt64, 480, 1, 48000, 0, 0, 48000), probeWA(0, 48000, 1, 48000, 1, 0), probeWA(4800, 48000, 1, 0, 1, 0), probeWA(4800, 48000, 1, 48000, 1, 2), probeWA(4800, 48000, 1, 48000, math.MaxUint64, 0),
		probeCA(0, 0, 48000), probeCA(1e9, 1, 0), probeCA(1e9, 1, math.NaN()), probeCA(math.MaxUint64, 0, 48000),
	} {
		if value != 0 {
			t.Fatal("invalid native timing was accepted", value)
		}
	}
}

func TestPipeWireDemandUsesQuantumNotBufferAllocation(t *testing.T) {
	if got := probePWFrames(0, 0, 1024, 1, 48000, 48000, 8192); got != 1024 {
		t.Fatal("allocation size mistaken for native quantum", got)
	}
	if got := probePWFrames(128, 0, 1024, 1, 48000, 48000, 8192); got != 128 {
		t.Fatal(got)
	}
	if got := probePWFrames(0, 941, 1024, 1, 48000, 44100, 8192); got != 941 {
		t.Fatal("rate-matched demand lost", got)
	}
	if got := probePWFrames(256, 0, 0, 0, 0, 48000, 128); got != 128 {
		t.Fatal("native capacity exceeded", got)
	}
	for _, got := range []uint32{probePWFrames(0, 0, 0, 1, 48000, 48000, 8192), probePWFrames(0, 0, 1024, 1, 48000, 44100, 8192), probePWFrames(0, 0, math.MaxUint64, math.MaxUint32, 48000, 48000, 8192), probePWFrames(0, 0, 1024, 1, 0, 48000, 8192)} {
		if got != 0 {
			t.Fatal("missing/fractional/overflowing demand fabricated", got)
		}
	}
}
