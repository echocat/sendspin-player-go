//go:build e2e

package audiotest

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestMissingDeviceOptInDoesNotConstructBackend(t *testing.T) {
	t.Setenv("SENDSPIN_FIXTURE_LOOPBACK_DEVICE", "")
	t.Setenv("SENDSPIN_FIXTURE_E2E_PLAYBACK", "1")
	t.Setenv("SENDSPIN_FIXTURE_E2E_VIRTUAL", "")
	t.Setenv("SENDSPIN_FIXTURE_E2E_DEVICE", "")
	t.Setenv("SENDSPIN_FIXTURE_SOURCE_E2E_DEVICE", "")
	output := func(string) audio.Output { t.Fatal("backend constructed without device opt-in"); return nil }
	input := func(string) audio.Input { t.Fatal("input constructed without device opt-in"); return nil }
	t.Run("loopback", func(t *testing.T) {
		RunNativeLoopback(t, LoopbackConfig{Prefix: "SENDSPIN_FIXTURE", OutputFactory: output, InputFactory: input})
	})
	t.Run("scenario", func(t *testing.T) {
		RunReferenceServerPlaybackScenario(t, ScenarioConfig{Prefix: "SENDSPIN_FIXTURE", OutputFactory: output})
	})
	t.Run("source", func(t *testing.T) {
		// Playback/monitor device ENV is not consent to capture a Source input.
		t.Setenv("SENDSPIN_FIXTURE_E2E_DEVICE", "not-source-consent")
		t.Setenv("SENDSPIN_FIXTURE_LOOPBACK_DEVICE", "not-source-consent")
		RunReferenceServerNativeInputScenario(t, SourceScenarioConfig{Prefix: "SENDSPIN_FIXTURE", InputFactory: input})
	})
}

// This is an offline synthetic oracle for the harness, NOT a native audio test.
func TestOfflineMarkerFixtureRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	tool := filepath.Join(dir, "loopback-tool")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	if err := buildLoopbackTool(ctx, repositoryRoot(t), tool); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(dir, "reference.wav")
	if err := exec.CommandContext(ctx, tool, "generate", "-out", reference, "-seconds", "3", "-amplitude", "0.01").Run(); err != nil {
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
	if err := exec.CommandContext(ctx, tool, "analyze", "-reference", reference, "-manifest", reference+".json", "-capture", path, "-source", "synthetic", "-out", filepath.Join(dir, "report.json")).Run(); err != nil {
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

func TestVirtualReferenceServerPlaybackScenario(t *testing.T) {
	if os.Getenv("SENDSPIN_AUDIOTEST_E2E_VIRTUAL") != "1" {
		t.Skip("explicit shared-fixture virtual scenario opt-in required; no server or device opened")
	}
	root := repositoryRoot(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tool := filepath.Join(dir, "loopback-tool")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	if err := buildLoopbackTool(ctx, root, tool); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(dir, "reference.wav")
	if err := exec.CommandContext(ctx, tool, "generate", "-out", reference, "-seconds", "3", "-amplitude", "0.01").Run(); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"SENDSPIN_AUDIOTEST_E2E_PLAYBACK": "", "SENDSPIN_AUDIOTEST_E2E_ROOT": root, "SENDSPIN_AUDIOTEST_E2E_ARTIFACT_DIR": dir, "SENDSPIN_AUDIOTEST_REFERENCE_FILE": reference} {
		t.Setenv(key, value)
	}
	RunReferenceServerPlaybackScenario(t, ScenarioConfig{Prefix: "SENDSPIN_AUDIOTEST", OutputFactory: func(string) audio.Output { t.Fatal("virtual fixture tried to construct native output"); return nil }})
}
