//go:build e2e

package audiotest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/e2etest"
	"github.com/echocat/sendspin-player-go/internal/loopback"
)

// LoopbackConfig never opens defaults: InputFactory receives only the separately
// authorized capture ID. Backend-specific monitor
// semantics belong to the factory, not this fixture.
type LoopbackConfig struct {
	Suite         *e2etest.Suite
	Prefix        string
	OutputFactory func(string) audio.Output
	InputFactory  func(string) audio.Input
}

func RunNativeLoopback(t *testing.T, config LoopbackConfig) {
	t.Helper()
	device, inputDevice := LoopbackDevices(t, config.Prefix)
	if config.Suite == nil {
		t.Fatal("native loopback requires an explicit package Suite")
	}
	private := privateDirectory(t)
	ctx, cancel := context.WithTimeout(config.Suite.Context(), 45*time.Second)
	defer cancel()
	runTool := func(args ...string) {
		t.Helper()
		// Never log raw recordings or upload these private temporary files.
		if err := loopback.Run(args, io.Discard); err != nil {
			t.Fatal("offline reference/marker analysis failed or measurement inconclusive:", err)
		}
	}
	reference := ReferenceWAV(t, private)
	// An explicit reference must carry its generator manifest; validate its
	// presence before opening capture or playback, never synthesize a fallback.
	if _, err := os.Stat(reference + ".json"); err != nil {
		t.Fatal("reference marker manifest required:", err)
	}
	samples, err := loadWeakReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	format := audio.Format{SampleRate: 48000, Channels: 2}
	// Output selection happens first, so the native caller can bind capture to
	// that selected backend instead of interpreting the same ID in another one.
	output, err := config.OutputFactory(device).Open(ctx, format)
	if err != nil {
		t.Fatal("explicit native output open failed; no fallback:", err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})
	input, err := config.InputFactory(inputDevice).Open(ctx, format)
	if err != nil {
		t.Fatal("explicit native input open failed; no fallback:", err)
	}
	t.Cleanup(func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
	})
	type recording struct {
		samples []float32
		err     error
	}
	captured := make(chan recording, 1)
	readCtx, stopRead := context.WithTimeout(ctx, 6*time.Second)
	defer stopRead()
	go func() {
		all := make([]float32, 0, 6*48000*2)
		buf := make([]float32, 960)
		for len(all) < 6*48000*2 {
			n, _, err := input.Read(readCtx, buf)
			if n < 0 || n > len(buf) || n%2 != 0 || (n == 0 && err == nil) {
				captured <- recording{err: errors.New("invalid native capture geometry/progress")}
				return
			}
			all = append(all, buf[:n]...)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
					err = nil
				}
				captured <- recording{all, err}
				return
			}
		}
		captured <- recording{samples: all}
	}()
	// Ensure failed playback never leaves a recording goroutine/device running.
	// InputDevice ownership requires Read to return before Close, including
	// failure cleanup; cancellation joins the reader before native disposal.
	defer func() { stopRead(); <-captured; _ = input.Close() }()
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	for len(samples) > 0 {
		if _, err := output.Next(ctx); err != nil {
			t.Fatal(err)
		}
		n := min(960, len(samples))
		if err := output.Write(ctx, samples[:n]); err != nil {
			t.Fatal(err)
		}
		samples = samples[n:]
	}
	var r recording
	select {
	case r = <-captured:
		captured <- r
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if r.err != nil {
		t.Fatal("native capture failed/discontinuous:", r.err)
	}
	if len(r.samples) < 5*48000*2 {
		t.Fatal("capture shorter than five seconds; measurement inconclusive")
	}
	capturePath := filepath.Join(private, "capture.wav")
	if err := writeCapture(capturePath, r.samples); err != nil {
		t.Fatal(err)
	}
	runTool("analyze", "-reference", reference, "-manifest", reference+".json", "-capture", capturePath, "-source", "recording", "-search-ms", "2000", "-out", filepath.Join(private, "analysis.json"))
	t.Log("Expected reference markers found in native capture. Unknown calibration/routing state: absolute latency and physical synchronization remain undetermined. Private WAVs are deleted, never uploaded.")
}

func loadWeakReference(path string) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	const size = 44 + 144000*4
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return nil, errors.New("reference must be exactly three seconds of generated PCM16 stereo")
	}
	b, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return nil, err
	}
	if len(b) != size || string(b[:4]) != "RIFF" || binary.LittleEndian.Uint32(b[4:]) != size-8 || string(b[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(b[16:]) != 16 || binary.LittleEndian.Uint16(b[20:]) != 1 || binary.LittleEndian.Uint16(b[22:]) != 2 || binary.LittleEndian.Uint32(b[24:]) != 48000 || binary.LittleEndian.Uint32(b[28:]) != 192000 || binary.LittleEndian.Uint16(b[32:]) != 4 || binary.LittleEndian.Uint16(b[34:]) != 16 || string(b[36:40]) != "data" || binary.LittleEndian.Uint32(b[40:]) != size-44 {
		return nil, errors.New("reference must be generated three-second 48kHz stereo PCM16 WAV")
	}
	samples := make([]float32, 144000*2)
	nonzero := false
	for i := range samples {
		v := int16(binary.LittleEndian.Uint16(b[44+i*2:]))
		if (i%2 == 1 && v != 0) || v < -328 || v > 328 {
			return nil, errors.New("reference must be weak left-only peak <=0.01 plus PCM16 rounding")
		}
		nonzero = nonzero || v != 0
		samples[i] = float32(v) / 32768
	}
	if !nonzero || !bytes.Equal(b[len(b)-48000/5*4:], make([]byte, 48000/5*4)) {
		return nil, errors.New("reference missing signal or silent tail")
	}
	return samples, nil
}

func writeCapture(path string, samples []float32) error {
	if len(samples)%2 != 0 {
		return errors.New("partial capture frame")
	}
	b := make([]byte, 44+len(samples)*2)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 2)
	binary.LittleEndian.PutUint32(b[24:], 48000)
	binary.LittleEndian.PutUint32(b[28:], 192000)
	binary.LittleEndian.PutUint16(b[32:], 4)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(samples)*2))
	for i, v := range samples {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return errors.New("nonfinite capture sample")
		}
		q := math.Round(math.Max(-1, math.Min(32767.0/32768, float64(v))) * 32768)
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(q)))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(b)
	if n != len(b) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	return errors.Join(writeErr, f.Close())
}
