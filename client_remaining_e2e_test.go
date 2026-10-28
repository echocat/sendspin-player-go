//go:build e2e

package sendspin

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
)

func TestReferenceNondefaultSettingsSurviveFileStoreClientRestart(t *testing.T) {
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := Secret{91}
	ref := startReferenceWithOptions(t, cred, key, false, map[string]any{"observe_controls": true})
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: key}}
	path := filepath.Join(t.TempDir(), "device.json")
	if err = (FileStore{Path: path}).Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Name: "Persistent nondefaults", Store: FileStore{Path: path}, Roles: []Role{Player}, Output: clockOutput{}, Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}}}
	first, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, first, ref)
	eventually(t, func() bool { return first.Snapshot().Available })
	if err = first.SetVolume(context.Background(), 17); err != nil {
		t.Fatal(err)
	}
	if err = first.SetMuted(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err = first.SetOutputDelay(context.Background(), 137*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	connection.cancel()
	select {
	case <-connection.done:
	case <-time.After(3 * time.Second):
		t.Fatal("first client did not release connection")
	}
	second, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := second.Snapshot()
	if state.Volume != 17 || !state.Muted || state.OutputDelayMS != 137 || first.ClientID() != second.ClientID() {
		t.Fatal("new FileStore-backed client lost settings/identity", state)
	}
	connectReference(t, second, ref)
	eventually(t, func() bool { return second.Snapshot().Available })
	eventually(t, func() bool {
		p := ref.control(t, map[string]any{"command": "player_status"})
		return p["volume"] == float64(17) && p["muted"] == true && p["output_delay_ms"] == float64(137)
	})
	stored, err := (FileStore{Path: path}).Load(context.Background())
	if err != nil || stored.Volume != 17 || !stored.Muted || stored.OutputDelayMS != 137 || !reflect.DeepEqual(stored.Records, cred.Records) {
		t.Fatal("reconnected settings differ from disk", err)
	}
}

func TestReferencePairedUnpairAlsoWorksWithoutAnyApplicationRole(t *testing.T) {
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := Secret{87}
	ref := startReference(t, cred, key, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: key}}
	store := &controlCountingStore{}
	if err = store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(context.Background(), Config{Name: "Roleless unpair", Store: store, Roles: []Role{}})
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	before := store.Saves.Load()
	ref.control(t, map[string]any{"command": "unpair"})
	select {
	case <-connection.done:
	case <-time.After(3 * time.Second):
		t.Fatal("roleless unpair did not close")
	}
	var goodbye *GoodbyeError
	if !errors.As(connection.err, &goodbye) || goodbye.Reason != "unpaired" {
		t.Fatal("wrong roleless unpair result", connection.err)
	}
	stored, err := store.Load(context.Background())
	if err != nil || len(stored.Records) != 0 || store.Saves.Load() != before+1 {
		t.Fatal("roleless unpair did not delete matched record", err)
	}
}

func TestReferenceValidUnavailableAudioIsDiscardedWithoutDisconnect(t *testing.T) {
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := Secret{90}
	ref := startReference(t, cred, key, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: key}}
	store := &MemoryStore{}
	if err = store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	output := &lifecycleProbe{entered: make(chan struct{}), release: make(chan struct{})}
	close(output.release)
	var encoded, decoded atomic.Int32
	client, err := New(context.Background(), Config{Name: "Unavailable valid audio", Store: store, Roles: []Role{Player, Controller}, Output: output, Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}}, Encoded: func(diagnostic.AudioChunk) { encoded.Add(1) }, Decoded: func(codec.Format, []float32) { decoded.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	if err = client.SetAvailable(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	pairingFixtureBarrier(t, client, ref, "unavailable-state-observed")
	// Deliberate crossed-lifecycle fixture: the client must process a valid
	// start/data sequence even though local availability has already changed.
	injectLifecycleStart(t, ref)
	injectLifecycleAudio(t, ref, lifecycleTimestamp(t, ref), 8192)
	injectLifecycleAudio(t, ref, lifecycleTimestamp(t, ref), 16384)
	pairingFixtureBarrier(t, client, ref, "unavailable-valid-audio-received")
	accepted, opens, _, _ := output.state()
	if encoded.Load() != 2 || decoded.Load() != 0 || opens != 0 || len(accepted) != 0 || client.Snapshot().Available || !slices.Contains(client.Snapshot().ActiveStreams, "player") {
		t.Fatal("unavailable audio was output or lost lifecycle state")
	}
	select {
	case <-connection.done:
		t.Fatal("valid unavailable audio disconnected", connection.err)
	default:
	}
	if err = client.SetAvailable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	injectLifecycleAudio(t, ref, lifecycleTimestamp(t, ref), 24576)
	pairingFixtureBarrier(t, client, ref, "fresh-audio-after-unavailable")
	eventually(t, func() bool { a, _, _, _ := output.state(); return len(a) > 0 })
	accepted, _, _, _ = output.state()
	if decoded.Load() != 1 || !slices.Equal(accepted, []float32{.75}) {
		t.Fatal("discarded unavailable audio reappeared", accepted)
	}
}

func TestReferenceSecondPairingAttemptIsAbortedClosedAndReleased(t *testing.T) {
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	a := startReferenceWithOptions(t, cred, cred.PairingPSK, true, map[string]any{"hold_pairing": true, "observe_pairing": true})
	b := startReferenceWithOptions(t, cred, cred.PairingPSK, true, map[string]any{"observe_pairing": true})
	store := &controlCountingStore{}
	if err = store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(context.Background(), Config{Name: "Competing pairing attempts", Store: store, Roles: []Role{Controller}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	a.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool { return a.control(t, map[string]any{"command": "status"})["pair_finalize_pending"] == true })
	before := store.Saves.Load()
	result := b.control(t, map[string]any{"command": "pair_at", "url": endpoint})
	if result["pair_abort"] != "concurrent_attempt" {
		t.Fatal("competing pairing was not explicitly aborted", result)
	}
	eventually(t, func() bool {
		if b.control(t, map[string]any{"command": "status"})["connected"] != false {
			return false
		}
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.sessions) == 1 && client.active != nil && client.active.channel.ServerID == a.id && client.active.pairingAttempt
	})
	pairingFixtureBarrier(t, client, a, "original-pairing-still-live")
	persisted, err := store.Load(context.Background())
	if err != nil || store.Saves.Load() != before || len(persisted.Records) != 0 || referenceEventCount(t, b, "pair_finalize", "") != 0 {
		t.Fatal("competing pairing disclosed/committed or changed holder", err)
	}
	a.control(t, map[string]any{"command": "release_pairing"})
	eventually(t, func() bool {
		return client.Snapshot().Available && a.control(t, map[string]any{"command": "status"})["pairing_record"] == true
	})
}

func TestReferenceSentinelCannotStartPairingPSK(t *testing.T) {
	client, ref, store, _ := pairingFieldClient(t, false, nil)
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	client.mu.Lock()
	category := client.active.channel.Category
	client.mu.Unlock()
	if category != "sn" {
		t.Fatal("fixture did not establish Sentinel")
	}
	sendPairingFixtureMessage(t, ref, "server/activate", map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "pairing_psk"}})
	eventually(t, func() bool { return referenceEventCount(t, ref, "pair_abort", "method_not_supported") == 1 })
	noPairingRecordOrReveal(t, store, ref)
	// The strict native peer may close after this injected activation: its own
	// state machine never requested pairing. This observation proves the actual
	// category refusal/no reveal, not that the peer accepts our deliberate fault.
}

func TestReferenceCancellingActivationAfterDisclosureDoesNotPersistPSK(t *testing.T) {
	for _, playback := range []bool{false, true} {
		name := "idle"
		if playback {
			name = "playback"
		}
		t.Run(name, func(t *testing.T) {
			cred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			ref := startReferenceWithOptions(t, cred, cred.PairingPSK, true, map[string]any{"hold_pairing": true, "observe_pairing": true})
			store := &controlCountingStore{}
			if err = store.Save(context.Background(), cred); err != nil {
				t.Fatal(err)
			}
			client, err := New(context.Background(), Config{Name: "Cancel after local disclosure", Store: store, Roles: []Role{Controller}, AllowUnpaired: true})
			if err != nil {
				t.Fatal(err)
			}
			connectReference(t, client, ref)
			eventually(t, func() bool {
				return ref.control(t, map[string]any{"command": "status"})["pair_finalize_pending"] == true
			})
			before := store.Saves.Load()
			activities := []string{}
			if playback {
				activities = []string{"playback"}
			}
			sendPairingFixtureMessage(t, ref, "server/activate", map[string]any{"activities": activities, "active_roles": []string{"controller@v1"}})
			pairingFixtureBarrier(t, client, ref, "cancellation-after-disclosure")
			sendPairingFixtureMessage(t, ref, "server/state", map[string]any{"controller": map[string]any{"supported_commands": []string{"play"}, "volume": 23, "muted": false, "repeat": "off", "shuffle": false}})
			pairingFixtureBarrier(t, client, ref, "replacement-controller-state")
			got, err := store.Load(context.Background())
			extra := int32(0)
			if playback {
				extra = 1
			}
			if err != nil || len(got.Records) != 0 || store.Saves.Load() != before+extra || client.Snapshot().Controller == nil || client.Snapshot().Controller.Volume != 23 || !slices.Equal(client.Snapshot().ActiveRoles, []Role{Controller}) {
				t.Fatal("cancellation persisted disclosed PSK or did not apply replacement", err)
			}
			if referenceEventCount(t, ref, "pair_finalize", "") != 1 {
				t.Fatal("fixture did not observe exactly one local disclosure")
			}
		})
	}
}

func TestReferenceDisplacedPreAttemptPairingSendsAbortAndCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	old := startReferenceWithOptions(t, cred, cred.PairingPSK, true, map[string]any{"observe_pairing": true})
	key := Secret{89}
	incoming := startReference(t, cred, key, false)
	cred.Records = []PairingRecord{{ServerID: incoming.id, PSK: key}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	hello, releaseHello := make(chan *session, 1), make(chan struct{})
	var once, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHello) }) }
	defer release()
	var client *Client
	client, err = New(ctx, Config{Name: "Pre-attempt retirement", Store: store, Roles: []Role{Controller}, DisableDiscovery: true, Observe: func(m diagnostic.Message) {
		if m.Direction != diagnostic.Sent || m.Type != "client/hello" {
			return
		}
		once.Do(func() {
			client.mu.Lock()
			var s *session
			for candidate := range client.sessions {
				if candidate.channel != nil && candidate.channel.ServerID == old.id {
					s = candidate
					break
				}
			}
			client.mu.Unlock()
			hello <- s
			select {
			case <-releaseHello:
			case <-ctx.Done():
			}
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	old.control(t, map[string]any{"command": "connect", "url": endpoint})
	var s *session
	select {
	case s = <-hello:
	case <-ctx.Done():
		t.Fatal("hello gate not reached")
	}
	if s == nil {
		t.Fatal("old session missing")
	}
	s.snapshotMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.snapshotMu.Unlock()
		}
	}()
	release()
	eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.active == s && !s.pairingAttempt
	})
	incoming.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return s.retiring })
	s.snapshotMu.Unlock()
	locked = false
	eventually(t, func() bool {
		return referenceEventCount(t, old, "pair_abort", "concurrent_attempt") == 1 && old.control(t, map[string]any{"command": "status"})["connected"] == false && client.Snapshot().ServerID == incoming.id
	})
	eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.sessions) == 1 })
	if referenceEventCount(t, old, "pair_init", "") != 0 || referenceEventCount(t, old, "pair_finalize", "") != 0 || old.control(t, map[string]any{"command": "status"})["pairing_record"] != false {
		t.Fatal("retired pairing started/disclosed/committed")
	}
	pairingFixtureBarrier(t, client, incoming, "replacement-after-pairing-abort")
}
