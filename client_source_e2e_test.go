//go:build e2e

package sendspin

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func sourceReferenceStatus(t *testing.T, ref referenceServer) map[string]any {
	t.Helper()
	return ref.control(t, map[string]any{"command": "source_status"})
}
func sourceReferenceStreams(t *testing.T, ref referenceServer) []any {
	t.Helper()
	return sourceReferenceStatus(t, ref)["streams"].([]any)
}
func sourceReferenceClient(t *testing.T, unpaired bool, combinedPlayer ...bool) (*Client, *fakeSourceInput, referenceServer) {
	t.Helper()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{143}
	ref := startReferenceWithOptions(t, cred, psk, false, map[string]any{"observe_source": true, "no_record": unpaired})
	if !unpaired {
		cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	}
	store := &MemoryStore{}
	if err = store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	input := newFakeSourceInput()
	cfg := Config{Name: "Explicit fake Source", Roles: []Role{SourceRole}, Store: store, Input: input, SourceFormat: audio.Format{SampleRate: 8000, Channels: 2}, AllowUnpaired: unpaired, DisableDiscovery: true}
	if len(combinedPlayer) > 0 && combinedPlayer[0] {
		cfg.Roles = append(cfg.Roles, Player)
		// Any local monitoring would open this deliberately failing output and
		// terminate the real session. Only server-distributed player audio may do so.
		cfg.Output = unusedOutput{}
	}
	client, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client, input, ref
}
func TestReferenceSourcePCMInputLifecycleAndReconnect(t *testing.T) {
	client, input, ref := sourceReferenceClient(t, false, true)
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	if input.opens.Load() != 0 {
		t.Fatal("handshake/availability activated hardware")
	}
	ref.control(t, map[string]any{"command": "source_start"})
	eventually(t, func() bool { return len(sourceReferenceStreams(t, ref)) == 1 })
	for range 3 {
		ref.control(t, map[string]any{"command": "source_start"})
	}
	var expected []byte
	for block := range 5 {
		samples := make([]float32, 320)
		for i := range samples {
			value := int16((block*320+i)%2001 - 1000)
			samples[i] = float32(value) / 32768
			expected = binary.LittleEndian.AppendUint16(expected, uint16(value))
		}
		input.reads <- sourceRead{samples: samples, first: time.Now().Add(-10 * time.Millisecond)}
		want := float64((block + 1) * 640)
		eventually(t, func() bool { return sourceReferenceStreams(t, ref)[0].(map[string]any)["bytes"] == want })
	}
	ref.control(t, map[string]any{"command": "source_stop"})
	eventually(t, func() bool {
		return sourceReferenceStreams(t, ref)[0].(map[string]any)["ended"] == true && input.closes.Load() == 1
	})
	streams := sourceReferenceStreams(t, ref)
	stream := streams[0].(map[string]any)
	digest := sha256.Sum256(expected)
	if len(streams) != 1 || stream["sha256"] != hex.EncodeToString(digest[:]) || stream["sample_rate"] != float64(8000) || stream["channels"] != float64(2) || stream["bit_depth"] != float64(16) || input.opens.Load() != 1 {
		t.Fatal("independent reference decoder did not receive exact interleaved PCM16", stream)
	}
	status := sourceReferenceStatus(t, ref)
	first, err := strconv.ParseInt(stream["first_timestamp"].(string), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	now, err := strconv.ParseInt(status["server_now"].(string), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if first > now || now-first > 2_000_000 {
		t.Fatal("source timestamp is not in independent server clock domain", first, now)
	}
	open := false
	chunks := 0
	for _, raw := range status["events"].([]any) {
		e := raw.(map[string]any)
		switch e["type"] {
		case "client/hello":
			if support, ok := e["support"].(map[string]any); !ok || len(support) != 0 {
				t.Fatal("missing/invented source capabilities")
			}
		case "client-stream/start":
			if open {
				t.Fatal("duplicate start")
			}
			open = true
		case "audio":
			if !open || e["bytes"] != float64(640) {
				t.Fatal("audio outside lifecycle or wrong chunk size")
			}
			chunks++
		case "client-stream/end":
			if !open {
				t.Fatal("duplicate end")
			}
			open = false
		}
	}
	if open || chunks != 5 {
		t.Fatal("source stream lifecycle/order incomplete", chunks)
	}
	// Local unavailability must send end before available:false on the same
	// encrypted FIFO. A start while unavailable does not authorize later capture.
	ref.control(t, map[string]any{"command": "source_start"})
	eventually(t, func() bool { return input.opens.Load() == 2 && len(sourceReferenceStreams(t, ref)) == 2 })
	if err = client.SetAvailable(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return sourceReferenceStreams(t, ref)[1].(map[string]any)["ended"] == true })
	sendPairingFixtureMessage(t, ref, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	pairingFixtureBarrier(t, client, ref, "source-unavailable-start-ignored")
	if err = client.SetAvailable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	pairingFixtureBarrier(t, client, ref, "source-ready-not-autostart")
	if input.opens.Load() != 2 {
		t.Fatal("availability transition resumed capture")
	}
	events := sourceReferenceStatus(t, ref)["events"].([]any)
	open = false
	for _, raw := range events {
		e := raw.(map[string]any)
		if e["type"] == "client-stream/start" {
			open = true
		}
		if e["type"] == "client-stream/end" {
			open = false
		}
		if e["type"] == "client/state" && e["available"] == false && open {
			t.Fatal("available:false preceded stream/end")
		}
	}
	// Disconnect an open stream: no old start survives a new connection.
	ref.control(t, map[string]any{"command": "source_start"})
	eventually(t, func() bool { return input.opens.Load() == 3 && len(sourceReferenceStreams(t, ref)) == 3 })
	connection.cancel()
	<-connection.done
	eventually(t, func() bool { return input.closes.Load() == 3 })
	connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	pairingFixtureBarrier(t, client, ref, "source-reconnected-no-start")
	if input.opens.Load() != 3 || slices.Contains(client.Snapshot().ActiveStreams, "source") {
		t.Fatal("reconnect reused start authorization")
	}
	ref.control(t, map[string]any{"command": "source_start"})
	eventually(t, func() bool { return input.opens.Load() == 4 && len(sourceReferenceStreams(t, ref)) == 4 })
	ref.control(t, map[string]any{"command": "source_stop"})
	eventually(t, func() bool { return input.closes.Load() == 4 })
}

func TestReferenceSourceRequiresExplicitUnpairedRoleApproval(t *testing.T) {
	client, input, ref := sourceReferenceClient(t, true)
	connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	ref.control(t, map[string]any{"command": "trust_unpaired"})
	pairingFixtureBarrier(t, client, ref, "ordinary-playback-approval-is-not-source-approval")
	if slices.Contains(client.Snapshot().ActiveRoles, SourceRole) || input.opens.Load() != 0 {
		t.Fatal("ordinary playback approval exposed source")
	}
	// Even a faulty start cannot turn an inactive source into implicit capture.
	sendPairingFixtureMessage(t, ref, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	pairingFixtureBarrier(t, client, ref, "inactive-source-start-ignored")
	ref.control(t, map[string]any{"command": "source_approve"})
	eventually(t, func() bool {
		return client.Snapshot().Available && slices.Contains(client.Snapshot().ActiveRoles, SourceRole)
	})
	if input.opens.Load() != 0 {
		t.Fatal("role approval alone opened input")
	}
	ref.control(t, map[string]any{"command": "source_start"})
	eventually(t, func() bool { return input.opens.Load() == 1 && len(sourceReferenceStreams(t, ref)) == 1 })
	ref.control(t, map[string]any{"command": "source_revoke"})
	eventually(t, func() bool {
		return !slices.Contains(client.Snapshot().ActiveRoles, SourceRole) && input.closes.Load() == 1
	})
	ref.control(t, map[string]any{"command": "source_approve"})
	eventually(t, func() bool { return slices.Contains(client.Snapshot().ActiveRoles, SourceRole) })
	pairingFixtureBarrier(t, client, ref, "source-reactivation-without-new-start")
	if input.opens.Load() != 1 {
		t.Fatal("reactivation reused recording authorization")
	}
}
