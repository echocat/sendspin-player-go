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
	"strings"
	"testing"
	"time"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

// SourceScenarioConfig binds every explicit start to the SAME explicitly
// consented input device. It cannot select output, enable monitoring or fallback.
type SourceScenarioConfig struct {
	Suite        *e2etest.Suite
	Prefix       string
	InputFactory func(device string) audio.Input
}

type sourceReceipt struct {
	Schema              int                    `json:"schema"`
	Status              string                 `json:"status"`
	Server              string                 `json:"independent_server"`
	Virtual             bool                   `json:"virtual"`
	SoftwareFixture     bool                   `json:"software_graph_fixture"`
	CaptureOpened       bool                   `json:"capture_opened"`
	OutputOpened        bool                   `json:"output_opened"`
	PhysicalConformance bool                   `json:"physical_conformance"`
	Scope               string                 `json:"scope"`
	Input               sourceInputStats       `json:"input"`
	DecodedFrames       int64                  `json:"decoded_frames"`
	Streams             []sourceObservedStream `json:"streams"`
}

func RunReferenceServerNativeInputScenario(t *testing.T, config SourceScenarioConfig) {
	t.Helper()
	runReferenceServerInputScenario(t, config, false)
}

// virtual is available only to package-internal fake-fixture tests. Native
// backend entrypoints never substitute a fake, noop, output or monitor input.
func runReferenceServerInputScenario(t *testing.T, config SourceScenarioConfig, virtual bool) *sourceReceipt {
	t.Helper()
	device := "synthetic-input-not-hardware"
	if !virtual {
		device = CaptureDevice(t, config.Prefix)
	}
	if config.Suite == nil {
		t.Fatal("source scenario requires an explicit package Suite")
	}
	dir := privateDirectory(t)
	ctx, cancel := context.WithTimeout(config.Suite.Context(), 60*time.Second)
	defer cancel()
	input := &sourceInputTap{factory: func() audio.Input { return config.InputFactory(device) }}
	receipt := sourceReceipt{Schema: 1, Status: "fail", Server: "aiosendspin[server]==10.2.1", Virtual: virtual, Scope: "Explicit input -> Source-only encrypted localhost session -> independent PCM16 decoder; counts, format, ordering, stop/close and broad clock-domain sanity only. No output/monitoring, signal-content, physical stereo, calibrated ADC/DAC latency or synchronization claim. No samples/WAVs retained."}
	completed := false
	defer func() {
		receipt.Input = input.stats()
		receipt.CaptureOpened = !virtual && receipt.Input.Opens > 0
		if completed && !t.Failed() {
			receipt.Status = "pass"
		}
		f, err := os.OpenFile(filepath.Join(dir, "source-e2e-report.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Error(err)
			return
		}
		encoder := json.NewEncoder(f)
		encoder.SetIndent("", "  ")
		if err := errors.Join(encoder.Encode(receipt), f.Close()); err != nil {
			t.Error(err)
		}
	}()
	credentials, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := credentials.PairingPSK
	ref := startScenarioReferenceWithOptions(t, config.Suite, credentials, key, map[string]any{"observe_source": true})
	credentials.Records = []sendspin.PairingRecord{{ServerID: ref.id, PSK: key}}
	store := &sendspin.MemoryStore{}
	if err := store.Save(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	// No Player role, Output or local monitor is configured at all.
	client, err := sendspin.New(ctx, sendspin.Config{Name: "Explicit input Source E2E", Store: store, Roles: []sendspin.Role{sendspin.SourceRole}, Input: input, SourceFormat: sourceCaptureFormat, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, stopClient := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- client.Run(clientCtx, strings.Replace(ref.url, "http://", "ws://", 1)+"/sendspin") }()
	defer func() {
		stopClient()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error("source SDK session failed:", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("source SDK worker failed to terminate")
		}
		stats := input.stats()
		if stats.Opens != stats.Closes || stats.Failures != 0 {
			t.Error("source input cleanup/validation failed", stats)
		}
	}()
	command := func(name string) map[string]any {
		t.Helper()
		result, err := ref.control(ctx, name)
		if err != nil {
			t.Fatal("independent source control failed:", err)
		}
		return result
	}
	status := func() sourceObservation {
		t.Helper()
		data, err := json.Marshal(command("source_status"))
		if err != nil {
			t.Fatal(err)
		}
		var result sourceObservation
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal("invalid source observation metadata:", err)
		}
		return result
	}
	wait := func(description string, condition func() bool) {
		t.Helper()
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			if input.failures.Load() != 0 {
				t.Fatal("source native input failed; no fallback", input.stats())
			}
			if condition() {
				return
			}
			select {
			case err := <-done:
				done <- err
				t.Fatal("source session ended while waiting for", description, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-deadline.C:
				t.Fatal("source timeout waiting for", description)
			case <-tick.C:
			}
		}
	}
	pause := func(duration time.Duration) {
		t.Helper()
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case err := <-done:
			done <- err
			t.Fatal("source session ended during bounded capture/idle window", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
	}
	wait("available Source-only role and server-observed ready source object", func() bool {
		snapshot := client.Snapshot()
		return snapshot.Available && slices.Equal(snapshot.ActiveRoles, []sendspin.Role{sendspin.SourceRole}) && status().ready()
	})
	if input.attempts.Load() != 0 {
		t.Fatal("input opened during handshake/availability without explicit start")
	}
	startUS, err := strconv.ParseInt(status().ServerNow, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	for cycle := 1; cycle <= 2; cycle++ {
		command("source_start")
		wait("one explicitly started native input and independently decoded PCM", func() bool {
			s := status()
			return input.opens.Load() == int64(cycle) && len(s.Streams) == cycle && s.Streams[cycle-1].Chunks > 0
		})
		pause(2 * time.Second)
		command("source_stop")
		wait("client-stream/end, independent decoder drain and native input close", func() bool {
			s := status()
			return len(s.Streams) == cycle && s.Streams[cycle-1].Ended && input.closes.Load() == int64(cycle) && !slices.Contains(client.Snapshot().ActiveStreams, "source")
		})
		stopped := input.stats()
		pause(300 * time.Millisecond)
		if input.stats() != stopped || stopped.Attempts != int64(cycle) {
			t.Fatal("input reopened/read after stop without a fresh explicit start")
		}
	}
	observed := status()
	frames, err := validateSourceObservation(observed, 2, startUS)
	if err != nil {
		t.Fatal(err)
	}
	stats := input.stats()
	if stats.Attempts != 2 || stats.Opens != 2 || stats.Closes != 2 || stats.Reads == 0 || stats.Samples < frames*2 || stats.Failures != 0 {
		t.Fatal("source input lifecycle/sample accounting failed", stats)
	}
	receipt.DecodedFrames, receipt.Streams = frames, observed.Streams
	completed = true
	t.Log("Two explicitly started/stopped input streams decoded independently as 48kHz/2-channel PCM16. No output, samples/WAVs or signal-content/calibrated-latency claims.")
	return &receipt
}
