//go:build e2e

// Package audiotest contains opt-in backend integration fixtures, never production code.
package audiotest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
)

type scenarioPhase struct {
	Name            string  `json:"name"`
	Codec           string  `json:"codec"`
	DurationSeconds float64 `json:"duration_seconds"`
	SourceFrames    int64   `json:"source_frames"`
	DecodedFrames   int64   `json:"decoded_frames"`
	OutputFrames    int64   `json:"output_frames"`
	SignalWrites    int64   `json:"signal_writes"`
}
type scenarioReceipt struct {
	Schema              int                 `json:"schema"`
	Status              string              `json:"status"`
	Virtual             bool                `json:"virtual_output"`
	SoftwareFixture     bool                `json:"software_graph_fixture"`
	Server              string              `json:"independent_server"`
	Scope               string              `json:"scope"`
	PhysicalConformance bool                `json:"physical_conformance"`
	CaptureOpened       bool                `json:"capture_opened"`
	Connections         int64               `json:"authenticated_sessions"`
	Phases              []scenarioPhase     `json:"phases"`
	Output              scenarioOutputStats `json:"output"`
}

// ScenarioConfig keeps backend construction lazy: the factory is called only
// after explicit opt-in and validation. No native failure falls back to a fake.
type ScenarioConfig struct {
	Prefix        string
	OutputFactory func(device string) audio.Output
	// SoftwareFixture uses short phases with a real private software graph,
	// never substitutes the output with a virtual clock.
	SoftwareFixture bool
}

func RunReferenceServerPlaybackScenario(t *testing.T, config ScenarioConfig) {
	t.Helper()
	prefix := config.Prefix
	live := os.Getenv(prefix+"_E2E_PLAYBACK") == "1"
	virtual := os.Getenv(prefix+"_E2E_VIRTUAL") == "1"
	if !live && !virtual {
		t.Skip("explicit native or virtual E2E opt-in required; no server or device opened")
	}
	if live && virtual {
		t.Fatal("native and virtual modes are mutually exclusive; no fallback")
	}
	device := os.Getenv(prefix + "_E2E_DEVICE")
	if live && device == "" {
		t.Skip(prefix + "_E2E_DEVICE required; no library, server or device opened")
	}
	root := os.Getenv(prefix + "_E2E_ROOT")
	dir := os.Getenv(prefix + "_E2E_ARTIFACT_DIR")
	if !filepath.IsAbs(root) || !filepath.IsAbs(dir) {
		t.Fatal("absolute repository and private artifact directory required")
	}
	reference := os.Getenv(prefix + "_REFERENCE_FILE")
	if !filepath.IsAbs(reference) {
		t.Fatal("absolute weak reference file required")
	}
	if _, err := loadWeakReference(reference); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	var base audio.Output
	if virtual {
		base = scenarioClockOutput{}
	} else {
		base = config.OutputFactory(device)
	}
	output := &scenarioOutput{base: base}
	receipt := scenarioReceipt{Schema: 1, Status: "running", Virtual: virtual, SoftwareFixture: config.SoftwareFixture, Server: "aiosendspin[server]==10.2.1", Scope: "Encrypted localhost session, PCM/FLAC decode/schedule/output, pause/resume, disconnect/reconnect and stop. No capture, physical latency, calibrated synchronization or analog stop-silence proof."}
	var connections atomic.Int64
	defer func() {
		receipt.Connections = connections.Load()
		receipt.Output = output.stats()
		receipt.Status = "pass"
		if t.Failed() {
			receipt.Status = "fail"
		}
		f, err := os.OpenFile(filepath.Join(dir, "e2e-report.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = f.Close() }()
		e := json.NewEncoder(f)
		e.SetIndent("", "  ")
		if err = e.Encode(receipt); err != nil {
			t.Error(err)
		}
	}()
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := cred.PairingPSK
	ref := startScenarioReference(t, ctx, root, dir, reference, cred, key)
	cred.Records = []sendspin.PairingRecord{{ServerID: ref.id, PSK: key}}
	store := &sendspin.MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	formats := []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16}, {Codec: "flac", SampleRate: 48000, Channels: 2, BitDepth: 16}}
	var decodedMu sync.Mutex
	decoded := map[string]int64{}
	var invalid atomic.Bool
	client, err := sendspin.New(ctx, sendspin.Config{Name: "Bounded backend E2E", Store: store, Output: output, Roles: []sendspin.Role{sendspin.Player}, Formats: formats, DisableDiscovery: true, MinBuffer: 150 * time.Millisecond, RequiredLeadTime: 250 * time.Millisecond,
		Decoded: func(f codec.Format, s []float32) {
			if f.SampleRate != 48000 || f.Channels != 2 {
				invalid.Store(true)
			}
			decodedMu.Lock()
			decoded[f.Codec] += int64(len(s) / 2)
			decodedMu.Unlock()
		},
		Observe: func(m diagnostic.Message) {
			if m.Direction == diagnostic.Received && m.Type == "server/hello" {
				connections.Add(1)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, stopClient := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- client.Run(clientCtx, strings.Replace(ref.url, "http://", "ws://", 1)+"/sendspin") }()
	defer func() {
		stopClient()
		select {
		case e := <-done:
			if e != nil && !errors.Is(e, context.Canceled) {
				t.Error(e)
			}
		case <-time.After(5 * time.Second):
			t.Error("SDK session did not terminate")
		}
		state := output.stats()
		if state.Opens != state.Closes || state.Failures != 0 {
			t.Error("native output cleanup failed", state)
		}
	}()
	wait := func(description string, condition func() bool) {
		t.Helper()
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			if condition() {
				return
			}
			select {
			case e := <-done:
				done <- e
				t.Fatalf("SDK session ended while waiting for %s: %v", description, e)
			case <-timer.C:
				t.Fatal("timeout waiting for", description)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-tick.C:
			}
		}
	}
	command := func(name string) map[string]any {
		t.Helper()
		r, e := ref.control(ctx, name)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	count := func(name string) int64 { decodedMu.Lock(); defer decodedMu.Unlock(); return decoded[name] }
	wait("authenticated available player", func() bool { return client.Snapshot().Available && connections.Load() == 1 })
	durations := []time.Duration{60 * time.Second, 30 * time.Second, 30 * time.Second, 60 * time.Second}
	if virtual || config.SoftwareFixture {
		durations = []time.Duration{3 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second}
	}
	phase := func(name, codecName string, duration time.Duration) {
		t.Helper()
		t.Logf("Phase %s: %.0fs %s", name, duration.Seconds(), codecName)
		before := output.stats()
		decodedBefore := count(codecName)
		sourceBefore := int64(command("weak_start")["source_frames"].(float64))
		started := time.Now()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		deadline := time.NewTimer(duration)
		defer deadline.Stop()
		last := before.Frames
		advanced := time.Now()
	loop:
		for {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case e := <-done:
				done <- e
				t.Fatal("SDK session ended during playback", e)
			case <-deadline.C:
				break loop
			case <-tick.C:
				state := output.stats()
				if state.Failures > 0 || invalid.Load() || !client.Snapshot().Available {
					t.Fatal("playback health check failed", state)
				}
				if state.Frames > last {
					last = state.Frames
					advanced = time.Now()
				}
				if time.Since(advanced) > 3*time.Second {
					t.Fatal("output stopped advancing")
				}
			}
		}
		sourceAfter := int64(command("weak_stop")["source_frames"].(float64))
		wait("stream stop/flush", func() bool {
			return !slices.Contains(client.Snapshot().ActiveStreams, "player") && output.flushes.Load() > before.Flushes
		})
		after := output.stats()
		entry := scenarioPhase{name, codecName, time.Since(started).Seconds(), sourceAfter - sourceBefore, count(codecName) - decodedBefore, after.Frames - before.Frames, after.SignalWrites - before.SignalWrites}
		receipt.Phases = append(receipt.Phases, entry)
		if entry.DecodedFrames < int64(duration.Seconds()*24000) || entry.OutputFrames < int64(duration.Seconds()*24000) || entry.SignalWrites == 0 {
			t.Fatal("insufficient decoded/native playback progress", entry)
		}
	}
	phase("initial", "pcm", durations[0])
	t.Log("Stopped stream; two-second pause, then new stream")
	timer := time.NewTimer(2 * time.Second)
	select {
	case <-ctx.Done():
		timer.Stop()
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	phase("resume", "pcm", durations[1])
	previous := connections.Load()
	command("weak_disconnect")
	wait("encrypted reconnect", func() bool { return connections.Load() > previous && client.Snapshot().Available })
	phase("reconnect", "pcm", durations[2])
	if err = client.PreferFormat(ctx, formats[1]); err != nil {
		t.Fatal(err)
	}
	wait("server FLAC selection", func() bool { r, e := ref.control(ctx, "weak_status"); return e == nil && r["codec"] == "flac" })
	phase("codec-change", "flac", durations[3])
	if connections.Load() != 2 {
		t.Fatal("unexpected extra reconnects", connections.Load())
	}
	if output.failures.Load() != 0 {
		t.Fatal("output operation failures", output.stats())
	}
	t.Log("PCM/FLAC playback, pause/resume, encrypted reconnect and stop completed; no analog timing claim")
}
