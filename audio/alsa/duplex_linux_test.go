//go:build linux && cgo

package alsa

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHardwareFullDuplexSilenceCapture(t *testing.T) {
	if os.Getenv("SENDSPIN_ALSA_DUPLEX_CAPTURE") != "1" {
		t.Skip("explicit capture consent required; no device or recorder opened")
	}
	device := os.Getenv("SENDSPIN_ALSA_DUPLEX_DEVICE")
	if !strings.HasPrefix(device, "hw:") || strings.ContainsRune(device, 0) {
		t.Fatal("explicit hw: device required; no default input fallback")
	}
	root := os.Getenv("SENDSPIN_ALSA_DUPLEX_ARTIFACT_DIR")
	if _, err := exec.LookPath("arecord"); err != nil {
		t.Fatal(err)
	}
	dir, capture, diagnostics, err := createDuplexArtifacts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capture.Close(); _ = diagnostics.Close() }()
	r := duplexResult{Schema: 1, Scope: "Explicit live full-duplex lifecycle: zero-only output plus microphone capture. No cable, level/mixer changes, latency, signal, flush-silence or physical conformance measurement.", Device: device, GOOS: runtime.GOOS, Seconds: 10, Rate: 48000, Channels: 2, Format: "S24_3LE", Status: "running"}
	// The recorder has an independent deadline even if a broken native output
	// call cannot be preempted. arecord is killed after 15s, never left recording.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "arecord", "--device", device, "--file-type", "wav", "--format", "S24_3LE", "--rate", "48000", "--channels", "2", "--duration", "10", "--disable-resample", "--disable-channels", "--disable-format", "--disable-softvol")
	cmd.Stdout, cmd.Stderr = capture, diagnostics
	recorder := &liveDuplexRecorder{cmd: cmd, started: &r.CaptureOpened, cancel: cancel}
	t.Log("Explicit ten-second microphone capture; output stays digital zero; private artifacts:", dir)
	r.ZeroFrames, r.Output, err = duplexLoop(ctx, (Output{Device: device}).Open, recorder)
	r.Status = "pass"
	if err == nil && r.ZeroFrames == 0 {
		err = errors.New("no confirmed zero frames submitted during capture")
	}
	if err == nil {
		r.CaptureFrames, r.CaptureBytes, err = captureGeometry(capture)
	}
	if err != nil {
		if stat, statErr := capture.Stat(); statErr == nil {
			r.CaptureBytes = stat.Size()
		}
	}
	if err != nil {
		r.Status = "fail"
		r.Error = err.Error()
	}
	if err = errors.Join(err, writeDuplexReport(dir, r)); err != nil {
		t.Fatal(err, "private diagnostics retained in", dir)
	}
	t.Logf("Full duplex completed: output=%+v, submitted zero frames=%d, capture frames=%d; no physical accuracy claim", r.Output, r.ZeroFrames, r.CaptureFrames)
	t.Log("Do not upload capture.wav: it may contain private microphone audio. Share report.json and terminal status only.")
}

type liveDuplexRecorder struct {
	cmd     *exec.Cmd
	started *bool
	cancel  context.CancelFunc
}

func (r *liveDuplexRecorder) Start() error {
	if err := r.cmd.Start(); err != nil {
		return err
	}
	*r.started = true
	return nil
}
func (r *liveDuplexRecorder) Wait() error { return r.cmd.Wait() }
func (r *liveDuplexRecorder) Stop()       { r.cancel() }
