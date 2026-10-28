//go:build e2e

package sendspin

import (
	"context"
	"encoding/base64"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/echocat/sendspin-player-go/diagnostic"
)

func referenceVisualizerProtocolClient(t *testing.T, config VisualizerConfig) (*Client, referenceServer, *mediaEvents, *[256]atomic.Int32, *outboundTestConnection) {
	t.Helper()
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{124}
	ref := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	counts := new([256]atomic.Int32)
	client, err := New(ctx, Config{Name: "Visualizer protocol", Store: store, Roles: []Role{VisualizerRole}, Visualizer: config, Observe: func(event diagnostic.Message) {
		if event.Direction == diagnostic.Received && event.HasMessageID && !event.Reassembled {
			counts[event.MessageID].Add(1)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	events := observeMedia(t, client)
	connection := connectReference(t, client, ref)
	eventually(t, func() bool {
		client.mu.Lock()
		s := client.active
		client.mu.Unlock()
		return s != nil && s.filter.Ready() && client.Snapshot().Available
	})
	return client, ref, events, counts, connection
}

func allVisualizerTypes() VisualizerConfig {
	return VisualizerConfig{Types: []string{"loudness", "beat", "f_peak", "spectrum", "peak"}, RateMax: 30, Spectrum: &Spectrum{Bins: 4, Scale: "lin", MinFrequency: 0, MaxFrequency: 20000}}
}

func TestReferenceVisualizerStreamConditionalSchemaClosesSilently(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"tracks-without-beat", map[string]any{"types": []string{"loudness"}, "rate_max": 30, "tracks_downbeats": false}},
		{"spectrum-without-type", map[string]any{"types": []string{}, "rate_max": 0, "spectrum": map[string]any{"n_disp_bins": 4, "scale": "lin", "f_min": 0, "f_max": 20000}}},
		{"missing-zero-minimum", map[string]any{"types": []string{"spectrum"}, "rate_max": 30, "spectrum": map[string]any{"n_disp_bins": 4, "scale": "lin", "f_max": 20000}}},
		{"missing-beat-metadata", map[string]any{"types": []string{"beat"}, "rate_max": 30}},
		{"unknown-type", map[string]any{"types": []string{"future"}, "rate_max": 30}},
		{"high-rate", map[string]any{"types": []string{}, "rate_max": 31}},
		{"wrong-spectrum", map[string]any{"types": []string{"spectrum"}, "rate_max": 30, "spectrum": map[string]any{"n_disp_bins": 3, "scale": "lin", "f_min": 0, "f_max": 20000}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ref, _, _, connection := referenceVisualizerProtocolClient(t, allVisualizerTypes())
			sendPairingFixtureMessage(t, ref, "stream/start", map[string]any{"server_transmitted": 0, "visualizer": tc.fields})
			requireSilentProtocolClose(t, connection, ref)
		})
	}
}

func TestReferenceVisualizerReservedBitsAndIDsAndStaleFrames(t *testing.T) {
	client, ref, events, counts, _ := referenceVisualizerProtocolClient(t, VisualizerConfig{Types: []string{"beat"}, RateMax: 30})
	mediaCommand(t, client, ref, map[string]any{"command": "visualizer_start", "tracks_downbeats": false})
	before := counts[17].Load()
	frame := mediaCommand(t, client, ref, map[string]any{"command": "visualizer_frame", "message_type": 17, "data_hex": "fe", "delay_ms": 500})
	stamp := int64(frame["timestamp"].(float64))
	if counts[17].Load() != before+1 {
		t.Fatal("reference frame was not actually received")
	}
	eventually(t, func() bool {
		for _, event := range events.snapshot() {
			if event.Visualization != nil && event.Visualization.Timestamp == stamp {
				return !event.Visualization.Downbeat && event.Visualization.Strength == 0 && event.Visualization.Values == nil
			}
		}
		return false
	})
	mediaCommand(t, client, ref, map[string]any{"command": "visualizer_clear"})
	before = counts[17].Load()
	stale := mediaCommand(t, client, ref, map[string]any{"command": "visualizer_frame", "message_type": 17, "data_hex": "00", "delay_ms": -1000})
	if counts[17].Load() != before+1 {
		t.Fatal("stale frame was dropped by the fixture instead of reaching the SDK")
	}
	staleStamp := int64(stale["timestamp"].(float64))
	// A later emitted visualization is an event-consumer FIFO barrier too;
	// a transport/group marker alone cannot prove the event goroutine drained.
	consumerBarrier := mediaCommand(t, client, ref, map[string]any{"command": "visualizer_frame", "message_type": 17, "data_hex": "00", "delay_ms": 100})
	consumerStamp := int64(consumerBarrier["timestamp"].(float64))
	eventually(t, func() bool {
		for _, event := range events.snapshot() {
			if event.Visualization != nil && event.Visualization.Timestamp == consumerStamp {
				return true
			}
		}
		return false
	})
	for _, event := range events.snapshot() {
		if event.Visualization != nil && event.Visualization.Timestamp == staleStamp {
			t.Fatal("stale visualization rendered")
		}
	}
	mediaCommand(t, client, ref, map[string]any{"command": "visualizer_end"})
	// Reserved sender IDs are unimplemented at this receiver. Core ignore
	// behavior still applies after activation, even without an active stream.
	for _, id := range []byte{21, 22, 23} {
		ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString([]byte{id})})
	}
	pairingFixtureBarrier(t, client, ref, "ignored-reserved-visualizer-ids")
	for _, id := range []byte{21, 22, 23} {
		if counts[id].Load() != 1 {
			t.Fatal("reserved ID not authenticated/received", id)
		}
	}
	if slices.Contains(client.Snapshot().ActiveStreams, "visualizer") {
		t.Fatal("unknown ID reopened a stream")
	}
}

func TestReferenceVisualizerDuplicateEchoAndUpdateKeepTimestampOrder(t *testing.T) {
	client, ref, events, _, connection := referenceVisualizerProtocolClient(t, VisualizerConfig{Types: []string{"loudness"}, RateMax: 30})
	mediaCommand(t, client, ref, map[string]any{"command": "visualizer_start"})
	sendPairingFixtureMessage(t, ref, "stream/start", map[string]any{"server_transmitted": 0, "visualizer": map[string]any{"types": []string{"loudness", "loudness"}, "rate_max": 20, "future": nil}})
	pairingFixtureBarrier(t, client, ref, "accepted-duplicate-visualizer-echo")
	first := mediaCommand(t, client, ref, map[string]any{"command": "visualizer_frame", "message_type": 16, "data_hex": "ffff", "delay_ms": 700})
	stamp := int64(first["timestamp"].(float64))
	eventually(t, func() bool {
		for _, event := range events.snapshot() {
			if event.Visualization != nil && event.Visualization.Timestamp == stamp {
				return slices.Equal(event.Visualization.Values, []uint16{65535})
			}
		}
		return false
	})
	// A valid in-place update must not reset the non-decreasing cursor.
	sendPairingFixtureMessage(t, ref, "stream/start", map[string]any{"server_transmitted": 0, "visualizer": map[string]any{"types": []string{"loudness"}, "rate_max": 10}})
	pairingFixtureBarrier(t, client, ref, "updated-visualizer-echo")
	ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString(visualizerFrame(16, stamp-1, 0, 0))})
	requireSilentProtocolClose(t, connection, ref)
}

func TestReferenceMalformedVisualizerPayloadsCloseSilently(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"short-header", []byte{16}},
		{"short-loudness", visualizerFrame(16, 0, 0)},
		{"long-peak", visualizerFrame(20, 0, 1, 2)},
		{"zero-frequency-with-amplitude", visualizerFrame(18, 0, 0, 0, 0, 1)},
		{"spectrum-count", visualizerFrame(19, 0, 0, 0)},
		{"unstreamed-beat", visualizerFrame(17, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, ref, _, _, connection := referenceVisualizerProtocolClient(t, allVisualizerTypes())
			mediaCommand(t, client, ref, map[string]any{"command": "visualizer_start"})
			ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString(tc.frame)})
			requireSilentProtocolClose(t, connection, ref)
		})
	}
}

func TestReferenceEmptyVisualizerTypesPermitStreamButNoKnownFrames(t *testing.T) {
	client, ref, _, _, connection := referenceVisualizerProtocolClient(t, VisualizerConfig{Types: []string{}, RateMax: 30})
	mediaCommand(t, client, ref, map[string]any{"command": "visualizer_start"})
	if !slices.Contains(client.Snapshot().ActiveStreams, "visualizer") {
		t.Fatal("empty negotiated set incorrectly ended stream")
	}
	ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString(visualizerFrame(16, 0, 0, 0))})
	requireSilentProtocolClose(t, connection, ref)
}
