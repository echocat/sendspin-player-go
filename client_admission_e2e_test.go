//go:build e2e

package sendspin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func TestE2EReferenceConcurrentInadmissibleActivationsCannotEnterAdmission(t *testing.T) {
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	holderKey, pairedKey, malformedKey := Secret{117}, Secret{118}, Secret{119}
	holder := startReference(t, credentials, holderKey, false)
	unauthorized := startReferenceWithOptions(t, credentials, pairedKey, false, map[string]any{"observe_pairing": true, "repeat_initial_activation": true, "initial_activation": map[string]any{
		"activities": []string{"playback", "pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "pairing_psk"},
	}})
	malformed := startReferenceWithOptions(t, credentials, malformedKey, false, map[string]any{"observe_pairing": true, "repeat_initial_activation": true, "initial_activation": map[string]any{
		"activities": []string{"playback"}, "active_roles": []string{"controller@v1", "controller@v1"},
	}})
	unpaired := startReferenceWithOptions(t, credentials, credentials.PairingPSK, false, map[string]any{"no_record": true, "observe_pairing": true, "repeat_initial_activation": true, "initial_activation": map[string]any{
		"activities": []string{"playback"}, "active_roles": []string{"controller@v1"},
	}})
	credentials.Records = []PairingRecord{{ServerID: holder.id, PSK: holderKey}, {ServerID: unauthorized.id, PSK: pairedKey}, {ServerID: malformed.id, PSK: malformedKey}}
	store := &controlCountingStore{}
	if err = store.Save(context.Background(), credentials); err != nil {
		t.Fatal(err)
	}
	client, err := New(context.Background(), Config{Name: "Inadmissible admission race", Store: store, Roles: []Role{Controller}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	holder.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == holder.id && client.Snapshot().Available })
	before := store.Saves.Load()
	// Valid contenders would block on this lock. All three invalid activations
	// must be refused independently, before any arbitration/displacement.
	client.admission.Lock()
	defer client.admission.Unlock()
	for _, peer := range []referenceServer{unauthorized, malformed, unpaired} {
		peer.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	}
	eventually(t, func() bool {
		return referenceEventCount(t, unauthorized, "disconnected", "unauthorized") > 0 && referenceEventCount(t, unpaired, "disconnected", "pairing_required") > 0 && referenceEventCount(t, malformed, "disconnected", "") > 0
	})
	for _, peer := range []referenceServer{unauthorized, malformed, unpaired} {
		peer.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	}
	// The reference connector may retry after a refusal. Fault injection must
	// remain invalid on those later connections too: otherwise an unrelated
	// valid retry can block on the deliberately held admission lock and use up
	// the fixture's provisional slots. Exercise that second generation directly.
	unauthorizedBefore := referenceEventCount(t, unauthorized, "disconnected", "unauthorized")
	malformedBefore := referenceEventCount(t, malformed, "disconnected", "")
	unpairedBefore := referenceEventCount(t, unpaired, "disconnected", "pairing_required")
	for _, peer := range []referenceServer{unauthorized, malformed, unpaired} {
		peer.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	}
	eventually(t, func() bool {
		return referenceEventCount(t, unauthorized, "disconnected", "unauthorized") > unauthorizedBefore && referenceEventCount(t, unpaired, "disconnected", "pairing_required") > unpairedBefore && referenceEventCount(t, malformed, "disconnected", "") > malformedBefore
	})
	for _, peer := range []referenceServer{unauthorized, malformed, unpaired} {
		peer.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	}
	eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.sessions) == 1
	})
	eventually(t, func() bool {
		for {
			select {
			case event := <-client.Events():
				if event.Kind == "disconnected" && errors.Is(event.Err, protocol.ErrProtocol) {
					return true
				}
			default:
				return false
			}
		}
	})
	pairingFixtureBarrier(t, client, holder, "holder-survived-invalid-contenders")
	if got := client.Snapshot(); got.ServerID != holder.id || !got.Available {
		t.Fatal("inadmissible peer displaced or deactivated the holder")
	}
	if referenceEventCount(t, holder, "disconnected", "") != 0 || referenceEventCount(t, unauthorized, "pair_init", "") != 0 || referenceEventCount(t, unpaired, "pair_init", "") != 0 {
		t.Fatal("refused activation interrupted the holder or disclosed pairing data")
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.Saves.Load() != before || persisted.LastPlaybackServer != holder.id || len(persisted.Records) != 3 {
		t.Fatal("refused activations mutated credentials or playback history")
	}
	for i, record := range credentials.Records {
		if persisted.Records[i] != record {
			t.Fatal("refusal changed a pairing record")
		}
	}
	client.mu.Lock()
	retiring := client.active.retiring
	client.mu.Unlock()
	if retiring {
		t.Fatal("rejected peer left the holder retiring")
	}
}

func TestE2EReferenceFullRecordStoreProtectsThreeUnfinishedHandshakes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	var peers []referenceServer
	for i := range 3 {
		key := Secret{byte(120 + i)}
		peer := startReference(t, credentials, key, false)
		peers = append(peers, peer)
		credentials.Records = append(credentials.Records, PairingRecord{ServerID: peer.id, PSK: key})
	}
	for i := range 2 {
		server, err := NewCredentials()
		if err != nil {
			t.Fatal(err)
		}
		credentials.Records = append(credentials.Records, PairingRecord{ServerID: server.ClientID(), PSK: Secret{byte(123 + i)}})
	}
	credentials.LastPlaybackServer = credentials.Records[4].ServerID
	pairing := startReferenceWithOptions(t, credentials, credentials.PairingPSK, true, map[string]any{"observe_pairing": true})
	store := &controlCountingStore{}
	if err = store.Save(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	selected := make(chan struct{}, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	defer resume()
	var initialResponses atomic.Int32
	client, err := New(ctx, Config{Name: "Full record protection", Store: store, Roles: []Role{Controller}, DisableDiscovery: true, RecordCapacity: 5,
		Observe: func(message diagnostic.Message) {
			if message.Direction == diagnostic.Sent && message.Type == "noise/handshake" && !message.Encrypted && initialResponses.Add(1) <= 3 {
				selected <- struct{}{}
				// The actual Noise response has been sent, but Handshake has not
				// returned its Channel. Hold all three record-backed sessions here.
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	for _, peer := range peers {
		peer.control(t, map[string]any{"command": "connect", "url": endpoint})
		select {
		case <-selected:
		case <-ctx.Done():
			t.Fatal("native handshake did not reach the selected-record gate")
		}
	}
	client.mu.Lock()
	protected := len(client.sessions) == 3
	for s := range client.sessions {
		protected = protected && s.channel == nil && s.recordServerID != ""
	}
	client.mu.Unlock()
	if !protected {
		t.Fatal("unfinished handshakes were not protected before Channel publication")
	}
	before := store.Saves.Load()
	pairing.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool {
		return client.Snapshot().ServerID == pairing.id && client.Snapshot().Available && pairing.control(t, map[string]any{"command": "status"})["pairing_record"] == true
	})
	persisted, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Records) != 5 || store.Saves.Load() != before+1 || persisted.LastPlaybackServer != credentials.LastPlaybackServer {
		t.Fatal("full-store pairing did not make exactly one bounded record transaction")
	}
	for i := range 3 {
		if persisted.Records[i] != credentials.Records[i] {
			t.Fatal("pairing evicted a provisional open connection's record", i)
		}
	}
	if persisted.Records[3] != credentials.Records[4] || persisted.Records[4].ServerID != pairing.id {
		t.Fatal("did not evict the oldest unprotected record")
	}
	client.mu.Lock()
	atCap := len(client.sessions) == 4
	client.mu.Unlock()
	if !atCap {
		t.Fatal("eviction test did not run at the concurrent session cap")
	}
	resume()
	for _, peer := range peers {
		eventually(t, func() bool { return referenceEventCount(t, peer, "disconnected", "concurrent_attempt") == 1 })
		peer.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	}
	eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.sessions) == 1 })
	pairingFixtureBarrier(t, client, pairing, "full-store-holder-still-usable")
}
