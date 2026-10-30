//go:build e2e && linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

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
