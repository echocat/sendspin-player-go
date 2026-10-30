//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestALSAInputDecodingPreservesSamplesWithoutGain(t *testing.T) {
	samples := []float32{0, .5, -.5, .25, -.25}
	for _, encoding := range []sampleEncoding{encodingFloat, encodingS32, encodingS24, encodingS24Packed, encodingS16} {
		encoded := encodeSamples(samples, encoding)
		got := make([]float32, len(samples))
		if err := decodeInput(got, encoded, encoding); err != nil {
			t.Fatal(err)
		}
		for i, v := range samples {
			if math.Abs(float64(got[i]-v)) > 1.0/32768 {
				t.Fatal("capture amplitude changed", encoding, i, got[i], v)
			}
		}
	}
}
func TestALSAInputExplicitLibraryAndCancellationAreInert(t *testing.T) {
	o := Input{SharedLibPath: filepath.Join(t.TempDir(), "absent-libasound.so")}
	if _, err := o.Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		t.Fatal("explicit input library failure was ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Open(ctx, audio.Format{SampleRate: 48000, Channels: 2}); err != context.Canceled {
		t.Fatal(err)
	}
}
