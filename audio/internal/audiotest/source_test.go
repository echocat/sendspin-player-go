//go:build e2e

package audiotest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

// This independent-server helper test is deliberately fake/software-only. Its
// ENV, name and receipt cannot be mistaken for any native backend input test.
func TestReferenceServerFakeInputScenario(t *testing.T) {
	if os.Getenv("SENDSPIN_AUDIOTEST_SOURCE_E2E_FAKE") != "1" {
		t.Skip("explicit software-only Source fixture opt-in required; no native input used")
	}
	artifacts := t.TempDir()
	t.Setenv("SENDSPIN_FAKE_SOURCE_E2E_DEVICE", "synthetic-input-not-hardware")
	t.Setenv("SENDSPIN_FAKE_SOURCE_E2E_ROOT", repositoryRoot(t))
	t.Setenv("SENDSPIN_FAKE_SOURCE_E2E_ARTIFACT_DIR", artifacts)
	var factories atomic.Int64
	runReferenceServerInputScenario(t, SourceScenarioConfig{Prefix: "SENDSPIN_FAKE", InputFactory: func(device string) audio.Input {
		factories.Add(1)
		if device != "synthetic-input-not-hardware" {
			return nil
		}
		return sourceFakeInput{}
	}}, true)
	if factories.Load() != 2 {
		t.Fatal("explicit source starts did not bind the same fake device twice")
	}
	files, err := filepath.Glob(filepath.Join(artifacts, "source-e2e-*", "source-e2e-report.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing software-only source receipt", err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var receipt sourceReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "pass" || !receipt.Virtual || receipt.CaptureOpened || receipt.OutputOpened || receipt.SoftwareFixture || receipt.DecodedFrames <= 0 || receipt.PhysicalConformance {
		t.Fatal("fake fixture mislabeled as native/physical evidence")
	}
}

type sourceFakeInput struct{}
type sourceFakeDevice struct{}

func (sourceFakeInput) Open(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if f != sourceCaptureFormat {
		return nil, errors.New("unexpected fake source format")
	}
	return sourceFakeDevice{}, nil
}
func (sourceFakeDevice) Read(ctx context.Context, dst []float32) (int, time.Time, error) {
	duration := time.Duration(len(dst)/2) * time.Second / 48000
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, time.Time{}, ctx.Err()
	case <-timer.C:
	}
	clear(dst)
	return len(dst), time.Now().Add(-duration), nil
}
func (sourceFakeDevice) Close() error { return nil }

type sourceStubInput struct {
	device audio.InputDevice
	err    error
}

func (i sourceStubInput) Open(context.Context, audio.Format) (audio.InputDevice, error) {
	return i.device, i.err
}

type sourceStubDevice struct {
	n      int
	at     time.Time
	err    error
	closes int
}

func (d *sourceStubDevice) Read(context.Context, []float32) (int, time.Time, error) {
	return d.n, d.at, d.err
}
func (d *sourceStubDevice) Close() error { d.closes++; return nil }

func TestSourceTapOwnsPartialOpensAndCountsWithoutSampleInspection(t *testing.T) {
	device := &sourceStubDevice{n: 4, at: time.Now()}
	factories := 0
	tap := &sourceInputTap{factory: func() audio.Input { factories++; return sourceStubInput{device: device} }}
	if factories != 0 {
		t.Fatal("input factory called before explicit Open")
	}
	for range 2 {
		d, err := tap.Open(context.Background(), sourceCaptureFormat)
		if err != nil {
			t.Fatal(err)
		}
		if n, _, err := d.Read(context.Background(), make([]float32, 4)); n != 4 || err != nil {
			t.Fatal("read accounting failed")
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if stats := tap.stats(); stats.Attempts != 2 || stats.Opens != 2 || stats.Closes != 2 || stats.Reads != 2 || stats.Samples != 8 || stats.Failures != 0 || factories != 2 || device.closes != 2 {
		t.Fatal("invalid tap lifecycle", stats)
	}
	partial := &sourceStubDevice{}
	partialTap := &sourceInputTap{factory: func() audio.Input { return sourceStubInput{device: partial, err: errors.New("partial open")} }}
	d, err := partialTap.Open(context.Background(), sourceCaptureFormat)
	if d == nil || err == nil {
		t.Fatal("partial native Open ownership lost")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if partial.closes != 1 || partialTap.stats().Failures != 1 {
		t.Fatal("partial Open not closed/counted")
	}
}

func TestSourceTapRejectsInvalidGeometryAndClockDomains(t *testing.T) {
	for _, device := range []*sourceStubDevice{{n: 3, at: time.Now()}, {n: 6, at: time.Now()}, {n: 4}, {n: 4, at: time.Now().Add(time.Hour)}} {
		tap := &sourceInputTap{factory: func() audio.Input { return sourceStubInput{device: device} }}
		d, err := tap.Open(context.Background(), sourceCaptureFormat)
		if err != nil {
			t.Fatal(err)
		}
		if n, _, err := d.Read(context.Background(), make([]float32, 4)); n != 0 || err == nil {
			t.Fatal("invalid geometry/domain accepted")
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		if tap.stats().Failures != 1 || tap.stats().Samples != 0 {
			t.Fatal("invalid read was credited")
		}
	}
	called := false
	tap := &sourceInputTap{factory: func() audio.Input { called = true; return nil }}
	if _, err := tap.Open(context.Background(), audio.Format{SampleRate: 8000, Channels: 2}); err == nil || called {
		t.Fatal("unexpected format initialized input")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tap.Open(ctx, sourceCaptureFormat); !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled Open initialized input")
	}
}

func validSourceObservation() sourceObservation {
	s := sourceObservation{ServerNow: "9000000", Events: []sourceObservedEvent{{Type: "client/hello", Support: json.RawMessage(`{}`)}, {Type: "client/state", Available: true, Source: json.RawMessage(`{}`)}}}
	for cycle := range 2 {
		start := sourceObservedEvent{Type: "client-stream/start"}
		start.Payload.Source = sourceWireFormat{"pcm", 48000, 2, 16}
		s.Events = append(s.Events, start)
		first := int64(6_000_000 + cycle*2_000_000)
		for block := range 50 {
			s.Events = append(s.Events, sourceObservedEvent{Type: "audio", Bytes: 3840, Timestamp: strconv.FormatInt(first+int64(block)*20000, 10)})
		}
		s.Events = append(s.Events, sourceObservedEvent{Type: "client-stream/end"})
		s.Streams = append(s.Streams, sourceObservedStream{SampleRate: 48000, Channels: 2, BitDepth: 16, Bytes: 192000, Chunks: 50, Ended: true, FirstTimestamp: strconv.FormatInt(first, 10)})
	}
	return s
}

func TestSourceObservationRejectsBadIndependentMetadataWithoutContentCriterion(t *testing.T) {
	if frames, err := validateSourceObservation(validSourceObservation(), 2, 5_000_000); err != nil || frames != 96000 {
		t.Fatal("valid wire geometry rejected", frames, err)
	}
	mutations := map[string]func(*sourceObservation){
		"format": func(s *sourceObservation) { s.Streams[0].SampleRate = 44100 },
		"audio-after-end": func(s *sourceObservation) {
			s.Events = append(s.Events, sourceObservedEvent{Type: "audio", Bytes: 3840, Timestamp: "8990000"})
		},
		"future-timestamp":   func(s *sourceObservation) { s.Events[3].Timestamp = "999999999999999999" },
		"repeated-timestamp": func(s *sourceObservation) { s.Events[4].Timestamp = s.Events[3].Timestamp },
		"partial-frame":      func(s *sourceObservation) { s.Events[3].Bytes = 3839 },
		"decoder-count":      func(s *sourceObservation) { s.Streams[0].Bytes += 4 },
		"decoder-domain":     func(s *sourceObservation) { s.Streams[0].FirstTimestamp = "6000001" },
		"not-drained":        func(s *sourceObservation) { s.Streams[0].Ended = false },
		"missing-end":        func(s *sourceObservation) { s.Events = s.Events[:len(s.Events)-1] },
		"not-ready":          func(s *sourceObservation) { s.Events[1].Source = json.RawMessage(`null`) },
		"invented-support":   func(s *sourceObservation) { s.Events[0].Support = json.RawMessage(`{"unexpected":true}`) },
		"withdrawn-before-start": func(s *sourceObservation) {
			s.Events = slices.Insert(s.Events, 2, sourceObservedEvent{Type: "client/state", Available: false, Source: json.RawMessage(`{}`)})
		},
		"withdrawn-before-end": func(s *sourceObservation) {
			s.Events = slices.Insert(s.Events, 4, sourceObservedEvent{Type: "client/state", Available: false, Source: json.RawMessage(`{}`)})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := validSourceObservation()
			mutate(&s)
			if _, err := validateSourceObservation(s, 2, 5_000_000); err == nil {
				t.Fatal("invalid source metadata accepted")
			}
		})
	}
}
