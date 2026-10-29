//go:build linux && cgo

package alsa

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestHardwareWeakReferencePlayback(t *testing.T) {
	if os.Getenv("SENDSPIN_ALSA_REFERENCE_PLAYBACK") != "1" {
		t.Skip("explicit weak-reference playback consent required; no device opened")
	}
	path := os.Getenv("SENDSPIN_ALSA_REFERENCE_FILE")
	if !filepath.IsAbs(path) {
		t.Fatal("explicit absolute reference file required")
	}
	samples, err := loadWeakReference(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	d, err := (Output{Device: "hw:CARD=S3,DEV=0"}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	t.Logf("SDK ALSA output: %+v; reference peak <=0.01 plus PCM16 rounding; left only", d.(Device).Info())
	frames, playErr := submitWeakReference(ctx, d, samples)
	if err = errors.Join(playErr, d.Close()); err != nil {
		t.Fatal(err)
	}
	if frames != 144000 {
		t.Fatalf("submitted %d reference frames, expected 144000", frames)
	}
	t.Log("SDK submitted all 144000 reference frames; Flush and Close completed. Silent tail may be dropped; no absolute latency or physical flush-silence claim.")
}
