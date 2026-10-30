//go:build e2e

package audiotest

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/e2etest"
	"github.com/echocat/sendspin-player-go/internal/loopback"
)

var suite = e2etest.NewSuite()

func TestMain(m *testing.M) { os.Exit(suite.Run(m)) }

// This is an offline synthetic oracle for the harness, NOT a native audio test.
func TestE2EOfflineMarkerFixtureRoundTrip(t *testing.T) {
	dir := privateDirectory(t)
	reference := filepath.Join(dir, "reference.wav")
	if err := loopback.Run([]string{"generate", "-out", reference, "-seconds", "3", "-amplitude", "0.01"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	samples, err := loadWeakReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	capture := append(make([]float32, 24000), samples...)
	capture = append(capture, make([]float32, 48000)...)
	path := filepath.Join(dir, "capture.wav")
	if err := writeCapture(path, capture); err != nil {
		t.Fatal(err)
	}
	if err := loopback.Run([]string{"analyze", "-reference", reference, "-manifest", reference + ".json", "-capture", path, "-source", "synthetic", "-out", filepath.Join(dir, "report.json")}, io.Discard); err != nil {
		t.Fatal("synthetic known-offset markers not found", err)
	}
	if err := writeCapture(path, capture); err == nil {
		t.Fatal("overwrote private recording")
	}
	for _, bad := range [][]float32{{0}, {float32(math.NaN()), 0}, {float32(math.Inf(1)), 0}} {
		if err := writeCapture(filepath.Join(dir, "invalid.wav"), bad); err == nil {
			t.Fatal("invalid capture accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "truncated.wav"), []byte("RIFF"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWeakReference(filepath.Join(dir, "truncated.wav")); err == nil {
		t.Fatal("invalid reference accepted")
	}
}

func TestE2EVirtualReferenceServerPlaybackScenario(t *testing.T) {
	runReferenceServerPlaybackScenario(t, ScenarioConfig{Suite: suite, OutputFactory: func(string) audio.Output {
		t.Fatal("virtual fixture tried to construct native output")
		return nil
	}}, true)
}

func TestE2EMissingDeviceConsentStopsBeforeBackendAndPeer(t *testing.T) {
	t.Setenv("SENDSPIN_TEST_AUDIO_BACKEND", "")
	t.Setenv(PlaybackDeviceEnv, "")
	t.Setenv(CaptureDeviceEnv, "")
	output := func(string) audio.Output { t.Fatal("backend constructed without consent"); return nil }
	input := func(string) audio.Input { t.Fatal("input constructed without consent"); return nil }
	// A nil Suite intentionally makes any accidental peer setup fail too.
	t.Run("playback", func(t *testing.T) {
		RunReferenceServerPlaybackScenario(t, ScenarioConfig{OutputFactory: output})
		t.Fatal("missing playback consent did not skip")
	})
	t.Run("weak-reference", func(t *testing.T) {
		RunWeakReferencePlayback(t, ScenarioConfig{OutputFactory: output})
		t.Fatal("missing playback consent did not skip weak-reference playback")
	})
	t.Run("loopback", func(t *testing.T) {
		t.Setenv(PlaybackDeviceEnv, "playback-is-not-capture-consent")
		RunNativeLoopback(t, LoopbackConfig{OutputFactory: output, InputFactory: input})
		t.Fatal("missing capture consent did not skip loopback")
	})
	t.Run("source", func(t *testing.T) {
		t.Setenv(PlaybackDeviceEnv, "playback-is-not-capture-consent")
		RunReferenceServerNativeInputScenario(t, SourceScenarioConfig{InputFactory: input})
		t.Fatal("missing capture consent did not skip source")
	})
	t.Run("full-duplex", func(t *testing.T) {
		t.Setenv(PlaybackDeviceEnv, "playback-is-not-capture-consent")
		RunFullDuplexSilenceCapture(t, LoopbackConfig{OutputFactory: output, InputFactory: input})
		t.Fatal("missing capture consent did not skip full duplex")
	})
}
