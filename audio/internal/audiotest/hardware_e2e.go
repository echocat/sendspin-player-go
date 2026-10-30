//go:build e2e

package audiotest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func RunWeakReferencePlayback(t *testing.T, config ScenarioConfig) {
	t.Helper()
	device := PlaybackDevice(t, config.Prefix)
	if config.Suite == nil {
		t.Fatal("weak-reference playback requires an explicit package Suite")
	}
	path := ReferenceWAV(t, privateDirectory(t))
	samples, err := loadWeakReference(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(config.Suite.Context(), 4*time.Second)
	defer cancel()
	d, err := config.OutputFactory(device).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal("explicit native output failed; no fallback:", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	frames := 0
	for len(samples) > 0 {
		if err = ctx.Err(); err != nil {
			t.Fatal(err)
		}
		if _, err = d.Next(ctx); err != nil {
			t.Fatal(err)
		}
		n := min(960, len(samples))
		if err = d.Write(ctx, samples[:n]); err != nil {
			t.Fatal(err)
		}
		frames += n / 2
		samples = samples[n:]
	}
	if err = errors.Join(d.Flush(ctx), d.Close()); err != nil {
		t.Fatal(err)
	}
	if frames != 144000 {
		t.Fatalf("submitted %d reference frames, expected 144000", frames)
	}
	t.Log("Go SDK submitted 144000 bounded left-only reference frames; Flush and Close completed. Silent tail may be dropped; no physical latency or flush-silence claim.")
}

func RunFullDuplexSilenceCapture(t *testing.T, config LoopbackConfig) {
	t.Helper()
	playback, capture := LoopbackDevices(t, config.Prefix)
	if config.Suite == nil {
		t.Fatal("full duplex requires an explicit package Suite")
	}
	ctx, cancel := context.WithTimeout(config.Suite.Context(), 15*time.Second)
	defer cancel()
	recorder := NewDuplexCapture(ctx, func() audio.Input { return config.InputFactory(capture) })
	frames, err := DuplexLoop(ctx, config.OutputFactory(playback).Open, recorder)
	if err == nil && (frames == 0 || recorder.Samples() != 10*48000*2) {
		err = errors.New("incomplete zero-only output or Go SDK input sample accounting")
	}
	if err != nil {
		t.Fatal("explicit full-duplex Go SDK lifecycle failed; no fallback:", err)
	}
	receipt := struct {
		Schema              int    `json:"schema"`
		Status              string `json:"status"`
		Scope               string `json:"scope"`
		CaptureOpened       bool   `json:"capture_opened"`
		ZeroFrames          int64  `json:"submitted_output_zero_frames"`
		CaptureFrames       int64  `json:"capture_frames"`
		PhysicalConformance bool   `json:"physical_conformance"`
	}{Schema: 1, Status: "pass", Scope: "Explicit native output and in-process Go SDK Input.Read lifecycle. Not an independent recorder. Digital-zero output, no level/mixer changes, retained samples/WAVs, physical signal, latency or synchronization claim.", CaptureOpened: recorder.Opened(), ZeroFrames: frames, CaptureFrames: recorder.Samples() / 2}
	f, err := os.OpenFile(filepath.Join(privateDirectory(t), "duplex-report.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(json.NewEncoder(f).Encode(receipt), f.Close()); err != nil {
		t.Fatal(err)
	}
	t.Logf("Go SDK full duplex completed: zero frames=%d, input frames=%d; no independent recorder or physical accuracy claim", frames, recorder.Samples()/2)
}
