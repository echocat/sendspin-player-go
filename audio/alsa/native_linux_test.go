//go:build linux && cgo

package alsa

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

// null is ALSA's discard-only software PCM, not a host device or audio server.
func TestNativeNullPCMConfigurationAndPluginPolicy(t *testing.T) {
	f := audio.Format{SampleRate: 48000, Channels: 2}
	open := func(ctx context.Context, o Output, f audio.Format) (pcm, negotiated, error) {
		return openNativeConfigured(ctx, o, f, "pcm.sendspin_test_null { type null }")
	}
	if _, err := openWith(context.Background(), Output{Device: "sendspin_test_null"}, f, open); !errors.Is(err, ErrPluginTiming) {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := openWith(ctx, Output{Device: "sendspin_test_null", AllowPluginTiming: true}, f, open)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	info := d.(*device).Info()
	if !info.Approximate || info.DriverType != "NULL" || info.SampleRate != 48000 || info.SampleFormat != "FLOAT_LE" {
		t.Fatal(info)
	}
	// null consumes without a physical clock. Test native ABI/lifecycle directly;
	// do not reinterpret its clock behavior as timing/conformance evidence.
	p := d.(*device).pcm
	if err = p.prepare(); err != nil {
		t.Fatal(err)
	}
	if n, err := p.write(make([]byte, 96*4), 48); err != nil || n != 48 {
		t.Fatal(n, err)
	}
	if err = p.start(); err != nil {
		t.Fatal(err)
	}
	if _, err = p.snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = d.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
}

// This opt-in writes zeros only. It never captures input or changes mixer state.
func TestHardwareSilenceLifecycle(t *testing.T) {
	name := os.Getenv("SENDSPIN_ALSA_SILENCE_DEVICE")
	if name == "" {
		t.Skip("explicit SENDSPIN_ALSA_SILENCE_DEVICE required; no device opened")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for range 3 {
		d, err := (Output{Device: name}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
		if err != nil {
			t.Fatal(err)
		}
		err = func() error {
			t.Logf("Negotiated: %+v; driver evidence only, no physical measurements", d.(Device).Info())
			if _, err := d.Next(ctx); err != nil {
				return err
			}
			for range 20 {
				if err := d.Write(ctx, make([]float32, 960)); err != nil {
					return err
				}
			}
			if err := d.Flush(ctx); err != nil {
				return err
			}
			if _, err := d.Next(ctx); err != nil {
				return err
			}
			canceled, stop := context.WithCancel(ctx)
			stop()
			if err := d.Write(canceled, make([]float32, 960)); !errors.Is(err, context.Canceled) {
				return errors.New("canceled write did not preserve cancellation")
			}
			return nil
		}()
		if err = errors.Join(err, d.Close()); err != nil {
			t.Fatal(err)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
