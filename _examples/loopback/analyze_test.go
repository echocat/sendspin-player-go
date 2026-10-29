package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
)

func testReference(t *testing.T, seconds int) (wave, manifest) {
	t.Helper()
	samples, m, err := referenceSignal(48000, seconds, 2, 0, .05, 73)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("offline deterministic unit fixture"))
	m.SHA256 = hex.EncodeToString(h[:])
	return wave{Rate: m.Rate, Channels: 2, Channel: 0, Bits: 16, ValidBits: 16, Encoding: "synthetic reference", SHA256: m.SHA256, Samples: samples}, m
}

// Independently construct sample coordinates with a known affine transform.
// This is simulation data, not native device/capture evidence.
func simulate(reference wave, offset, ppm, gain, noise, dc float64) wave {
	scale := 1 + ppm/1e6
	n := int(math.Ceil(float64(len(reference.Samples))*scale+offset)) + reference.Rate/3
	out := wave{Rate: reference.Rate, Channels: 1, Channel: 0, Bits: 32, ValidBits: 32, Encoding: "synthetic capture", SHA256: strings.Repeat("a", 64), Samples: make([]float32, n)}
	state := uint64(91823)
	for i := range out.Samples {
		x := (float64(i) - offset) / scale
		at := int(math.Floor(x))
		value := 0.0
		if at >= 0 && at+1 < len(reference.Samples) {
			f := x - float64(at)
			value = (1-f)*float64(reference.Samples[at]) + f*float64(reference.Samples[at+1])
		}
		state = state*6364136223846793005 + 1442695040888963407
		random := float64(state>>11)/float64(uint64(1)<<53)*2 - 1
		out.Samples[i] = float32(gain*value + noise*random + dc)
	}
	return out
}

func TestAffineAlignmentRecoversKnownFractionalOffsetAndRelativeClock(t *testing.T) {
	reference, m := testReference(t, 14)
	for _, test := range []struct {
		name                         string
		offset, ppm, gain, noise, dc float64
	}{
		{"integer", 1234, 0, 1, 0, 0},
		{"fractional-fast", 1234.25, 250, .35, .0003, .01},
		{"negative-offset-inverted", -200.4, -180, -.6, .0002, -.02},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := simulate(reference, test.offset, test.ppm, test.gain, test.noise, test.dc)
			o := defaultOptions()
			o.Source = "synthetic"
			r, err := analyze(context.Background(), m, reference, capture, o)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != "aligned" || r.Fit == nil {
				t.Fatal("synthetic alignment failed", r.Warnings, r.Detections)
			}
			if math.Abs(r.Fit.FileOffsetFrames-test.offset) > .5 || math.Abs(r.Fit.RelativePPM-test.ppm) > 1 {
				t.Fatal("wrong affine fit", *r.Fit, test)
			}
			if r.PhysicalConformance || r.HardwareOpened || r.AbsoluteOutputLatencyMS != nil {
				t.Fatal("offline fit invented hardware/absolute latency evidence")
			}
			if len(r.DriftWindows) < 1 || r.Products > maxProducts {
				t.Fatal("missing windows or unbounded correlation")
			}
		})
	}
}

func TestMissingAndAmbiguousMarkersCannotProduceSuccessfulAlignment(t *testing.T) {
	reference, m := testReference(t, 8)
	for _, name := range []string{"missing", "duplicate", "silence", "full-scale"} {
		t.Run(name, func(t *testing.T) {
			capture := simulate(reference, 1200, 0, 1, 0, 0)
			switch name {
			case "missing":
				p := m.Markers[3]
				clear(capture.Samples[1200+p.Start : 1200+p.Start+p.Frames])
			case "duplicate":
				p := m.Markers[0]
				copy(capture.Samples[1200+p.Start+4096:], reference.Samples[p.Start:p.Start+p.Frames])
			case "silence":
				clear(capture.Samples)
			case "full-scale":
				capture.Samples[0] = 1
				capture.FullScale = 1
			}
			r, err := analyze(context.Background(), m, reference, capture, defaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != "inconclusive" {
				t.Fatal("bad/ambiguous capture passed", name, r.Detections)
			}
			if name == "duplicate" && r.Detections[0].Status != "ambiguous" {
				t.Fatal("distinct competing correlation peak was hidden", r.Detections[0])
			}
		})
	}
}

func TestClockDiscontinuityAndSignalTailRemainVisible(t *testing.T) {
	reference, m := testReference(t, 12)
	capture := simulate(reference, 800, 0, 1, 0, 0)
	for i := len(capture.Samples) - 1; i > 800+6*reference.Rate+60; i-- {
		capture.Samples[i] = capture.Samples[i-60]
	}
	for i := 800 + len(reference.Samples); i < len(capture.Samples); i++ {
		capture.Samples[i] = float32(.02 * math.Sin(2*math.Pi*1000*float64(i)/float64(reference.Rate)))
	}
	o := defaultOptions()
	o.MaxResidualMS = .2
	r, err := analyze(context.Background(), m, reference, capture, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "inconclusive" || r.Fit == nil || r.Fit.MaxResidualMS <= o.MaxResidualMS {
		t.Fatal("discontinuity hidden", r.Warnings, r.Fit)
	}
	if r.Tail == nil || r.Tail.ACRMS < .005 {
		t.Fatal("tail energy was hidden", r.Tail)
	}
}

func TestProvenanceRatesCancellationAndWorkAreBounded(t *testing.T) {
	reference, m := testReference(t, 3)
	capture := simulate(reference, 500, 0, 1, 0, 0)
	bad := m
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := analyze(context.Background(), bad, reference, capture, defaultOptions()); err == nil {
		t.Fatal("mismatched manifest accepted")
	}
	otherRate := capture
	otherRate.Rate = 44100
	if _, err := analyze(context.Background(), m, reference, otherRate, defaultOptions()); err == nil {
		t.Fatal("silently resampled WAV inputs")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analyze(ctx, m, reference, capture, defaultOptions()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	budget := int64(1)
	if _, _, _, err := locate(context.Background(), capture.Samples, reference.Samples[m.Markers[0].Start:m.Markers[0].Start+markerFrames], 0, 100, &budget); !errors.Is(err, errWorkBudget) {
		t.Fatal("unbounded work", err)
	}
}
