package loopback

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIWorkflowAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	if root := os.Getenv("SENDSPIN_LOOPBACK_ARTIFACT_DIR"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("artifact directory must be absolute")
		}
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		var err error
		dir, err = os.MkdirTemp(root, "synthetic-")
		if err != nil {
			t.Fatal(err)
		}
		t.Log("retaining synthetic-only offline fixture:", dir)
	}
	ref := filepath.Join(dir, "reference.wav")
	capture := filepath.Join(dir, "synthetic.wav")
	result := filepath.Join(dir, "report.json")
	var stdout bytes.Buffer
	if err := Run([]string{"generate", "-out", ref, "-seconds", "4"}, &stdout); err != nil {
		t.Fatal(err)
	}
	w, err := readWave(context.Background(), ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	m, err := loadManifest(ref + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.validate(w); err != nil {
		t.Fatal(err)
	}
	synthetic := simulate(w, 900, 100, .5, 0, 0)
	if _, err = writeWave(context.Background(), capture, 48000, 1, 0, synthetic.Samples); err != nil {
		t.Fatal(err)
	}
	args := []string{"analyze", "-reference", ref, "-manifest", ref + ".json", "-capture", capture, "-source", "synthetic", "-out", result}
	if err = Run(args, &stdout); err != nil {
		t.Fatal(err, stdout.String())
	}
	b, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	var report report
	if err = json.Unmarshal(b, &report); err != nil || report.Status != "aligned" || report.PhysicalConformance || report.AbsoluteOutputLatencyMS != nil {
		t.Fatal("bad offline report", err)
	}
	t.Logf("synthetic-only truth: offset=900 frames, relative ratio=100 ppm; observed=%+v", report.Fit)
	truth := []byte("{\"source\":\"synthetic\",\"expected_offset_frames\":900,\"expected_relative_ppm\":100,\"physical_conformance\":false}\n")
	if err = os.WriteFile(filepath.Join(dir, "synthetic-truth.json"), truth, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Run(args, &stdout); err == nil {
		t.Fatal("report overwritten")
	}
	after, _ := os.ReadFile(result)
	if !bytes.Equal(b, after) {
		t.Fatal("existing report changed")
	}
	if err = Run([]string{"generate", "-out", ref, "-seconds", "4"}, &stdout); err == nil {
		t.Fatal("reference overwritten")
	}
}

func TestCLIHasNoImplicitActionOrUnsafeGeneration(t *testing.T) {
	for _, args := range [][]string{nil, {"capture"}, {"play"}, {"generate"}, {"analyze"}, {"generate", "-out", filepath.Join(t.TempDir(), "bad.wav"), "-amplitude", "1"}, {"generate", "-out", filepath.Join(t.TempDir(), "bad.wav"), "-seconds", "999999999"}} {
		var stdout bytes.Buffer
		if err := Run(args, &stdout); err == nil {
			t.Fatal("invalid/implicit command accepted", args)
		}
	}
}

func TestCLIHelp(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"short", []string{"-h"}, "No device access, playback or capture."},
		{"long", []string{"--help"}, "No device access, playback or capture."},
		{"command", []string{"help"}, "No device access, playback or capture."},
		{"generate", []string{"generate", "-h"}, "Required new WAV path; existing files are never overwritten"},
		{"analyze", []string{"analyze", "-h"}, "Required recording/synthetic WAV; no recording is started"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			if err := Run(tt.args, &stdout); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("help missing %q: stdout=%q", tt.want, stdout.String())
			}
		})
	}
}
