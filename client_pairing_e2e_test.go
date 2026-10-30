//go:build e2e

package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func pairingFieldClient(t *testing.T, nativePairing bool, options map[string]any) (*Client, referenceServer, *MemoryStore, *outboundTestConnection) {
	t.Helper()
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"no_record": true, "observe_pairing": true}
	for key, value := range options {
		settings[key] = value
	}
	ref := startReferenceWithOptions(t, cred, cred.PairingPSK, nativePairing, settings)
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Pairing fields", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	return client, ref, store, connectReference(t, client, ref)
}

func sendPairingFixtureMessage(t *testing.T, ref referenceServer, kind string, payload any) {
	t.Helper()
	ref.control(t, map[string]any{"command": "send", "message": map[string]any{"type": kind, "payload": payload}})
}

func pairingFixtureBarrier(t *testing.T, client *Client, ref referenceServer, name string) {
	t.Helper()
	sendPairingFixtureMessage(t, ref, "group/update", map[string]any{"playback_state": "stopped", "group_id": name, "group_name": name})
	eventually(t, func() bool { return client.Snapshot().Group.ID == name })
}

func noPairingRecordOrReveal(t *testing.T, store *MemoryStore, ref referenceServer) {
	t.Helper()
	cred, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cred.Records) != 0 || referenceEventCount(t, ref, "pair_init", "") != 0 || referenceEventCount(t, ref, "pair_finalize", "") != 0 {
		t.Fatal("rejected/canceled attempt disclosed or persisted a new credential")
	}
	if ref.control(t, map[string]any{"command": "status"})["pairing_record"] != false {
		t.Fatal("reference persisted a rejected attempt")
	}
}

func requirePairInitIndex(t *testing.T, ref referenceServer, want ...int) {
	t.Helper()
	count := 0
	for _, raw := range ref.control(t, map[string]any{"command": "status"})["events"].([]any) {
		event := raw.(map[string]any)
		if event["type"] == "pair_init" {
			if count >= len(want) {
				t.Fatal("observed an unexpected additional attempt")
			}
			if event["pairing_index"] != float64(want[count]) {
				t.Fatalf("pairing index %v, want %d", event["pairing_index"], want[count])
			}
			count++
		}
	}
	if count != len(want) {
		t.Fatalf("observed %d PSK attempt starts, want %d", count, len(want))
	}
}

func requireSilentProtocolClose(t *testing.T, connection *outboundTestConnection, ref referenceServer) {
	t.Helper()
	select {
	case <-connection.done:
	case <-time.After(3 * time.Second):
		t.Fatal("protocol violation left connection open")
	}
	if !errors.Is(connection.err, protocol.ErrProtocol) {
		t.Fatal("expected protocol error", connection.err)
	}
	eventually(t, func() bool { return referenceEventCount(t, ref, "disconnected", "") > 0 })
	for _, raw := range ref.control(t, map[string]any{"command": "status"})["events"].([]any) {
		event := raw.(map[string]any)
		if event["type"] == "wire_goodbye" || (event["type"] == "disconnected" && event["reason"] != nil) {
			t.Fatal("silent failure sent an application goodbye", event)
		}
	}
}

func TestE2EReferenceIgnoresInactivePairingField(t *testing.T) {
	client, ref, store, _ := pairingFieldClient(t, false, nil)
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	for _, value := range []any{nil, false, 42, "unused", []any{}, map[string]any{"method": nil, "format": false}} {
		sendPairingFixtureMessage(t, ref, "server/activate", map[string]any{"activities": []string{}, "pairing": value})
	}
	pairingFixtureBarrier(t, client, ref, "ignored-pairing-fields")
	noPairingRecordOrReveal(t, store, ref)
}

func TestE2EReferenceMalformedPairingActivationClosesSilently(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
	}{
		{"missing-object", map[string]any{"activities": []string{"pairing"}}},
		{"null-object", map[string]any{"activities": []string{"pairing"}, "pairing": nil}},
		{"array-object", map[string]any{"activities": []string{"pairing"}, "pairing": []any{}}},
		{"missing-method", map[string]any{"activities": []string{"pairing"}, "pairing": map[string]any{}}},
		{"null-method", map[string]any{"activities": []string{"pairing"}, "pairing": map[string]any{"method": nil}}},
		{"numeric-method", map[string]any{"activities": []string{"pairing"}, "pairing": map[string]any{"method": 4}}},
		{"null-format", map[string]any{"activities": []string{"pairing"}, "pairing": map[string]any{"method": "pairing_psk", "format": nil}}},
		{"numeric-format", map[string]any{"activities": []string{"pairing"}, "pairing": map[string]any{"method": "pairing_psk", "format": 4}}},
		{"missing-dynamic-format", map[string]any{"activities": []string{"pairing"}, "pairing": map[string]any{"method": "dynamic_pairing_code"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, ref, store, connection := pairingFieldClient(t, false, nil)
			eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
			sendPairingFixtureMessage(t, ref, "server/activate", tc.payload)
			requireSilentProtocolClose(t, connection, ref)
			if referenceEventCount(t, ref, "pair_abort", "") != 0 {
				t.Fatal("malformed fields were reported as unsupported method")
			}
			noPairingRecordOrReveal(t, store, ref)
		})
	}
}

func TestE2EReferencePairingAbortDiscardStopsAtNextActivation(t *testing.T) {
	client, ref, store, connection := pairingFieldClient(t, true, map[string]any{
		"initial_activation": map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "dynamic_pairing_code", "format": "digits"}},
		"pairing_stale_messages": []any{
			map[string]any{"type": "server/pair-auth", "payload": map[string]any{"pake_msg_1": nil}},
			map[string]any{"type": "server/pair-finalize", "payload": map[string]any{}},
		},
	})
	eventually(t, func() bool { return referenceEventCount(t, ref, "pair_abort", "method_not_supported") == 1 })
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	pairingFixtureBarrier(t, client, ref, "discard-window-ended")
	noPairingRecordOrReveal(t, store, ref)
	sendPairingFixtureMessage(t, ref, "server/pair-finalize", map[string]any{})
	requireSilentProtocolClose(t, connection, ref)
}

func TestE2EReferenceUnsupportedFirstPairingActivationCanRecover(t *testing.T) {
	client, ref, store, _ := pairingFieldClient(t, true, map[string]any{"initial_activation": map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "dynamic_pairing_code", "format": "digits"}}})
	eventually(t, func() bool { return referenceEventCount(t, ref, "pair_abort", "method_not_supported") == 1 })
	noPairingRecordOrReveal(t, store, ref)
	// The native reference ends its rejected attempt with an idle activation.
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	pairingFixtureBarrier(t, client, ref, "recovered-initial-activation")
	// Native pairing must still work, using the independent server's normal
	// re-handshake, pairing-index checks, finalize, persistence and promotion.
	ref.control(t, map[string]any{"command": "pair"})
	eventually(t, func() bool { return client.Snapshot().Available })
	cred, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cred.Records) != 1 || cred.Records[0].ServerID != ref.id || cred.Records[0].PSK == cred.PairingPSK {
		t.Fatal("native repair did not install a distinct bound key")
	}
	if referenceEventCount(t, ref, "pair_init", "") != 1 || referenceEventCount(t, ref, "pair_finalize", "") != 1 {
		t.Fatal("incorrect PSK attempt sequence")
	}
	requirePairInitIndex(t, ref, 2)
}

func TestE2EReferenceUnsupportedRehandshakeActivationCanRecover(t *testing.T) {
	client, ref, store, _ := pairingFieldClient(t, false, map[string]any{"pairing_activation": map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "pairing_psk", "format": "digits"}}})
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	result := ref.control(t, map[string]any{"command": "pair"})
	if result["pair_abort"] != "method_not_supported" {
		t.Fatal("native server did not observe format refusal", result)
	}
	pairingFixtureBarrier(t, client, ref, "recovered-rehandshake-activation")
	noPairingRecordOrReveal(t, store, ref)
	ref.control(t, map[string]any{"command": "pair"})
	eventually(t, func() bool { return client.Snapshot().Available })
	cred, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cred.Records) != 1 || cred.Records[0].ServerID != ref.id {
		t.Fatal("second native attempt did not persist the bound record")
	}
	requirePairInitIndex(t, ref, 2)
}

func TestE2EReferencePSKPairingRejectsAnEmissionFormat(t *testing.T) {
	client, ref, store, _ := pairingFieldClient(t, true, map[string]any{"initial_activation": map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "pairing_psk", "format": "digits"}}})
	eventually(t, func() bool { return referenceEventCount(t, ref, "pair_abort", "method_not_supported") == 1 })
	noPairingRecordOrReveal(t, store, ref)
	sendPairingFixtureMessage(t, ref, "server/activate", map[string]any{"activities": []string{}, "active_roles": []string{}})
	pairingFixtureBarrier(t, client, ref, "psk-format-rejected")
}

func TestE2EReferencePSKPairingIgnoresUnknownParameterFields(t *testing.T) {
	client, ref, store, _ := pairingFieldClient(t, true, map[string]any{"initial_activation": map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "pairing_psk", "future_info": nil, "FORMAT": false}}})
	eventually(t, func() bool { return client.Snapshot().Available })
	cred, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cred.Records) != 1 || cred.Records[0].ServerID != ref.id {
		t.Fatal("extension prevented native pairing")
	}
	requirePairInitIndex(t, ref, 1)
	// Re-pair the same, now-long-term-keyed socket. This must re-handshake
	// onto the standing pairing PSK, reset the index to one and replace just
	// the old server record with a fresh long-term key.
	ref.control(t, map[string]any{"command": "pair"})
	eventually(t, func() bool { return client.Snapshot().Available })
	again, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Records) != 1 || again.Records[0].ServerID != ref.id || again.Records[0].PSK == cred.Records[0].PSK || again.PairingPSK != cred.PairingPSK || again.PrivateKey != cred.PrivateKey {
		t.Fatal("re-pairing did not replace only the long-term record")
	}
	requirePairInitIndex(t, ref, 1, 1)
}

func TestE2EReferenceUnauthorizedPrecedesUnsupportedPairing(t *testing.T) {
	_, ref, store, connection := pairingFieldClient(t, false, map[string]any{"initial_activation": map[string]any{"activities": []string{"playback", "pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "dynamic_pairing_code", "format": "digits"}}})
	select {
	case <-connection.done:
	case <-time.After(3 * time.Second):
		t.Fatal("inadmissible activation stayed open")
	}
	var goodbye *GoodbyeError
	if !errors.As(connection.err, &goodbye) || goodbye.Reason != "unauthorized" {
		t.Fatal("wrong refusal priority", connection.err)
	}
	eventually(t, func() bool {
		return referenceGoodbyeObserved(t, ref, "unauthorized") && referenceEventCount(t, ref, "disconnected", "") == 1
	})
	if referenceEventCount(t, ref, "pair_abort", "") != 0 {
		t.Fatal("sent pairing abort before required goodbye")
	}
	noPairingRecordOrReveal(t, store, ref)
}

func TestE2EReferencePSKPairingRejectsCodeMessagesMidAttempt(t *testing.T) {
	for _, kind := range []string{"server/pair-init", "server/pair-auth", "server/pair-confirm"} {
		t.Run(kind, func(t *testing.T) {
			client, ref, store, connection := pairingFieldClient(t, true, map[string]any{"hold_pairing": true})
			eventually(t, func() bool {
				return ref.control(t, map[string]any{"command": "status"})["pair_finalize_pending"] == true
			})
			sendPairingFixtureMessage(t, ref, kind, map[string]any{})
			select {
			case <-connection.done:
			case <-time.After(3 * time.Second):
				t.Fatal("out-of-sequence code message left the client connected")
			}
			if !errors.Is(connection.err, protocol.ErrProtocol) {
				t.Fatal("expected protocol error", connection.err)
			}
			cred, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(cred.Records) != 0 || ref.control(t, map[string]any{"command": "status"})["pairing_record"] != false {
				t.Fatal("out-of-sequence message persisted the pending record")
			}
			if client.Snapshot().ServerID != "" {
				t.Fatal("failed connection remained admitted")
			}
			// The reference's disconnect event waits for the deliberately paused
			// Store operation. Release it only after proving the SDK closed and
			// persisted nothing. A remote Store may commit after the socket closed;
			// this is not evidence that the SDK acknowledged/persisted its record.
			ref.control(t, map[string]any{"command": "release_pairing"})
			requireSilentProtocolClose(t, connection, ref)
		})
	}
}

func TestE2EReferenceDisplacementSurvivesRejectedRehandshakeActivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	a := startReferenceWithOptions(t, cred, cred.PairingPSK, false, map[string]any{
		"no_record": true, "observe_pairing": true, "hold_followup_activation": true,
		"pairing_activation": map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "pairing_psk", "format": "digits"}},
	})
	t.Cleanup(func() { a.control(t, map[string]any{"command": "release_activation"}) })
	pskB := Secret{119}
	b := startReference(t, cred, pskB, false)
	cred.Records = []PairingRecord{{ServerID: b.id, PSK: pskB}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	sent, received := make(chan struct{}), make(chan struct{})
	releaseSend, releaseReceive := make(chan struct{}), make(chan struct{})
	var sendOnce, receiveOnce, releaseSendOnce, releaseReceiveOnce sync.Once
	releaseFirst := func() { releaseSendOnce.Do(func() { close(releaseSend) }) }
	releaseSecond := func() { releaseReceiveOnce.Do(func() { close(releaseReceive) }) }
	defer releaseFirst()
	defer releaseSecond()
	client, err := New(ctx, Config{Name: "Deferred displacement", Store: store, Roles: []Role{Controller}, DisableDiscovery: true, Observe: func(event diagnostic.Message) {
		if event.Direction == diagnostic.Sent && event.Encrypted && event.Type == "noise/handshake" {
			sendOnce.Do(func() {
				close(sent)
				select {
				case <-releaseSend:
				case <-ctx.Done():
				}
			})
		}
		if event.Direction == diagnostic.Received && event.Type == "server/activate" {
			var payload struct {
				Activities []string `json:"activities"`
			}
			_ = json.Unmarshal(event.Payload, &payload)
			for _, activity := range payload.Activities {
				if activity == "pairing" {
					receiveOnce.Do(func() {
						close(received)
						select {
						case <-releaseReceive:
						case <-ctx.Done():
						}
					})
				}
			}
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	a.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool { return client.Snapshot().ServerID == a.id })
	client.mu.Lock()
	old := client.active
	client.mu.Unlock()
	// Pairing waits through a real re-handshake. The HTTP request may end in
	// either a pairing abort or disconnect when the other server takes over.
	pairCtx, pairCancel := context.WithCancel(ctx)
	request, err := http.NewRequestWithContext(pairCtx, http.MethodPost, a.url+"/control", strings.NewReader(`{"command":"pair"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	pairDone := make(chan struct{})
	go func() {
		defer close(pairDone)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	t.Cleanup(func() {
		pairCancel()
		select {
		case <-pairDone:
		case <-time.After(3 * time.Second):
			t.Error("pairing HTTP request did not stop")
		}
	})
	select {
	case <-sent:
	case <-ctx.Done():
		t.Fatal("re-handshake did not reach send barrier")
	}
	b.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	// The old actor is still inside its send callback, so displacement must
	// queue before it can finish re-handshaking and defer that decision.
	eventually(t, func() bool { return len(old.displace) == 1 })
	releaseFirst()
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("new-key activation did not reach receive barrier")
	}
	// Arrival is held at the reader. Once the actor has consumed displace,
	// it must finish that synchronous handler before it can apply activation.
	eventually(t, func() bool { return len(old.displace) == 0 })
	releaseSecond()
	// Keep any native follow-up activation gated until takeover succeeds:
	// finishing the handoff must not depend on the old server sending one.
	eventually(t, func() bool { return client.Snapshot().ServerID == b.id && client.Snapshot().Available })
	a.control(t, map[string]any{"command": "release_activation"})
	eventually(t, func() bool {
		return referenceGoodbyeObserved(t, a, "another_server") && referenceEventCount(t, a, "disconnected", "") > 0
	})
	a.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	if referenceEventCount(t, a, "pair_abort", "method_not_supported") != 1 || referenceEventCount(t, a, "pair_finalize", "") != 0 {
		t.Fatal("displaced unsupported attempt was started instead of refused")
	}
	persisted, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Records) != 1 || persisted.Records[0].ServerID != b.id {
		t.Fatal("displaced attempt changed persisted records")
	}
}
