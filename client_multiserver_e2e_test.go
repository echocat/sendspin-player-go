//go:build e2e

package sendspin

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func referenceEventCount(t *testing.T, ref referenceServer, kind, reason string) int {
	t.Helper()
	status := ref.control(t, map[string]any{"command": "status"})
	count := 0
	for _, raw := range status["events"].([]any) {
		event := raw.(map[string]any)
		if event["type"] == kind && (reason == "" || event["reason"] == reason) {
			count++
		}
	}
	return count
}

func referenceArbitrationReady(t *testing.T, client *Client, ref referenceServer, playback bool) {
	t.Helper()
	// Available is published before client/state is written. The reference must
	// consume that state and release its controller snapshot before another peer
	// competes with it. Observe both directions instead of timing the handshake.
	eventually(t, func() bool {
		state := client.Snapshot()
		if state.ServerID != ref.id || !state.Available || state.Controller == nil || !slices.Equal(state.ActiveRoles, []Role{Controller}) {
			return false
		}
		status := ref.control(t, map[string]any{"command": "status"})
		return status["connected"] == true && status["available"] == true && slices.Equal(status["active_roles"].([]any), []any{string(Controller)})
	})
	// A same-direction marker drains the role queues and proves that Go has
	// processed their preceding messages, without changing playback activity.
	marker := ref.control(t, map[string]any{"command": "barrier"})["marker"]
	eventually(t, func() bool { return client.Snapshot().Group.Name == marker })
	wantRank := 0
	if playback {
		wantRank = 2
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.active == nil || client.active.channel.ServerID != ref.id || client.active.rank != wantRank {
		t.Fatal("reference activity changed before arbitration")
	}
}

func referenceFirstHeldRefusal(t *testing.T, client *Client, ref referenceServer, endpoint string) {
	t.Helper()
	eventually(t, func() bool { return referenceEventCount(t, ref, "activation_send_held", "") == 1 })
	eventually(t, func() bool {
		for {
			select {
			case event := <-client.Events():
				if event.Kind == "disconnected" && logErrorClass(event.Err) == "concurrent_attempt" {
					return true
				}
			default:
				return false
			}
		}
	})
	ref.control(t, map[string]any{"command": "release_activation_send"})
	eventually(t, func() bool { return referenceEventCount(t, ref, "disconnected", "") > 0 })
	// Stop the connector before inspecting counters. Native reason=None can
	// schedule a redial; waiting for an exact counter while it runs is racy.
	ref.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	if got := referenceEventCount(t, ref, "wire_goodbye", "concurrent_attempt"); got != 1 {
		t.Fatalf("first refused connection: got %d wire goodbyes with concurrent_attempt, want 1", got)
	}
}

func TestE2EReferencePairingProtectionStartsBeforeFirstWriteReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	pairing := startReferenceWithOptions(t, cred, cred.PairingPSK, true, map[string]any{"hold_pairing": true})
	psk := Secret{125}
	playback := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: playback.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	defer resume()
	client, err := New(ctx, Config{Name: "Pair-init admission race", Store: store, Roles: []Role{Controller}, DisableDiscovery: true, Observe: func(message diagnostic.Message) {
		// Noise bodies are a separate type. The first encrypted redacted
		// control message here is the native PSK flow's actual pair-init.
		if message.Direction == diagnostic.Sent && message.Encrypted && message.Type == "redacted/control" {
			enterOnce.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	pairing.control(t, map[string]any{"command": "connect", "url": endpoint})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("pair-init did not reach its transmit barrier")
	}
	playback.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return referenceEventCount(t, playback, "wire_goodbye", "concurrent_attempt") > 0 })
	eventually(t, func() bool { return referenceEventCount(t, playback, "disconnected", "") > 0 })
	if client.Snapshot().ServerID != pairing.id {
		t.Fatal("already transmitted pair-init was displaced")
	}
	playback.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	resume()
	eventually(t, func() bool {
		return pairing.control(t, map[string]any{"command": "status"})["pair_finalize_pending"] == true
	})
	pairing.control(t, map[string]any{"command": "release_pairing"})
	eventually(t, func() bool { return client.Snapshot().ServerID == pairing.id && client.Snapshot().Available })
	playback.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == playback.id && client.Snapshot().Available })
}

func TestE2EReferenceMultiServerArbitration(t *testing.T) {
	for _, tc := range []struct {
		name                                                               string
		initialPlayback, candidatePlayback, preferCandidate, wantCandidate bool
		holdAfterActivation                                                bool
	}{
		{name: "idle-keeps-current"},
		{name: "goodbye-before-reader-start", holdAfterActivation: true},
		{name: "idle-prefers-last-playback", preferCandidate: true, wantCandidate: true},
		{name: "playback-preempts-idle", candidatePlayback: true, wantCandidate: true},
		{name: "equal-playback-replaces-current", initialPlayback: true, candidatePlayback: true, wantCandidate: true},
		{name: "idle-cannot-preempt-playback", initialPlayback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			pskA, pskB := Secret{11}, Secret{22}
			a := startReference(t, cred, pskA, false)
			b := startReferenceWithOptions(t, cred, pskB, false, map[string]any{"hold_after_activation": tc.holdAfterActivation})
			cred.Records = []PairingRecord{{ServerID: a.id, PSK: pskA}, {ServerID: b.id, PSK: pskB}}
			if tc.preferCandidate {
				cred.LastPlaybackServer = b.id
			}
			store := &MemoryStore{}
			if err = store.Save(ctx, cred); err != nil {
				t.Fatal(err)
			}
			client, err := New(ctx, Config{Name: "Arbitration E2E", Store: store, Roles: []Role{Controller}, DisableDiscovery: true})
			if err != nil {
				t.Fatal(err)
			}
			endpoint, _ := serveTestClient(t, client)
			t.Cleanup(func() {
				if t.Failed() {
					state := client.Snapshot()
					t.Logf("arbitration: holder=%s available=%t roles=%v controller=%t; initial=%s candidate=%s", state.ServerID, state.Available, state.ActiveRoles, state.Controller != nil, a.id, b.id)
					t.Logf("initial reference: %v", a.control(t, map[string]any{"command": "status"}))
					t.Logf("candidate reference: %v", b.control(t, map[string]any{"command": "status"}))
					for {
						select {
						case event := <-client.Events():
							t.Logf("client event: kind=%s server=%s error_class=%s", event.Kind, event.ServerID, logErrorClass(event.Err))
						default:
							return
						}
					}
				}
			})
			a.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": tc.initialPlayback})
			referenceArbitrationReady(t, client, a, tc.initialPlayback)
			b.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": tc.candidatePlayback})
			if tc.holdAfterActivation {
				// Let the client finish its refusal while the reference is still
				// returning from server/activate, before its normal reader starts.
				referenceFirstHeldRefusal(t, client, b, endpoint)
				for _, raw := range b.control(t, map[string]any{"command": "status"})["events"].([]any) {
					event := raw.(map[string]any)
					if event["type"] == "wire_goodbye" {
						t.Logf("first refusal: native decoder observed reason=%v receive_path=%v", event["reason"], event["receive_path"])
					} else if event["type"] == "disconnected" {
						t.Logf("first refusal: native disconnect callback reason=%v", event["reason"])
					}
				}
			}
			if tc.wantCandidate {
				referenceArbitrationReady(t, client, b, tc.candidatePlayback)
				eventually(t, func() bool { return referenceEventCount(t, a, "wire_goodbye", "another_server") > 0 })
				eventually(t, func() bool { return referenceEventCount(t, a, "disconnected", "") > 0 })
			} else {
				eventually(t, func() bool { return referenceEventCount(t, b, "wire_goodbye", "concurrent_attempt") > 0 })
				eventually(t, func() bool { return referenceEventCount(t, b, "disconnected", "") > 0 })
				if got := client.Snapshot().ServerID; got != a.id {
					t.Fatal("rejected candidate displaced current server", got)
				}
				b.control(t, map[string]any{"command": "disconnect", "url": endpoint})
				referenceArbitrationReady(t, client, a, tc.initialPlayback)
			}
			persisted, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := cred.LastPlaybackServer
			if tc.initialPlayback {
				want = a.id
			}
			if tc.wantCandidate && tc.candidatePlayback {
				want = b.id
			}
			if persisted.LastPlaybackServer != want {
				t.Fatal("incorrect persisted last-playback server")
			}
			if len(persisted.Records) != 2 {
				t.Fatal("arbitration modified pairing records")
			}
		})
	}
	t.Run("readiness-includes-controller-state", referenceArbitrationReadinessIncludesControllerState)
	t.Run("concurrent-peers-keep-controller-observations-isolated", referenceConcurrentPeersKeepControllerObservationsIsolated)
}

func referenceArbitrationReadinessIncludesControllerState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	pskA, pskB := Secret{61}, Secret{62}
	a := startReference(t, cred, pskA, false)
	b := startReferenceWithOptions(t, cred, pskB, false, map[string]any{"hold_after_activation": true})
	cred.Records = []PairingRecord{{ServerID: a.id, PSK: pskA}, {ServerID: b.id, PSK: pskB}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	defer resume()
	client, err := New(ctx, Config{Name: "Arbitration readiness", Store: store, Roles: []Role{Controller}, DisableDiscovery: true, Observe: func(message diagnostic.Message) {
		if message.Direction == diagnostic.Received && message.Type == "server/state" {
			enterOnce.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	a.control(t, map[string]any{"command": "connect", "url": endpoint})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reference controller state did not reach its receive barrier")
	}
	// This is the old readiness check, deliberately held before the controller
	// snapshot is delivered. No scheduler delay or fabricated activation is used.
	state := client.Snapshot()
	if state.ServerID != a.id || !state.Available || state.Controller != nil {
		t.Fatal("receive barrier did not expose the incomplete initial snapshot")
	}
	resume()
	referenceArbitrationReady(t, client, a, false)
	b.control(t, map[string]any{"command": "connect", "url": endpoint})
	referenceFirstHeldRefusal(t, client, b, endpoint)
	referenceArbitrationReady(t, client, a, false)
	persisted, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LastPlaybackServer != "" || !slices.Equal(persisted.Records, cred.Records) {
		t.Fatal("idle arbitration changed playback history or pairing records")
	}
}

func referenceConcurrentPeersKeepControllerObservationsIsolated(t *testing.T) {
	t.Helper()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	commands := []string{"play", "pause", "next", "previous"}
	refs := make([]referenceServer, len(commands))
	clients := make([]*Client, len(commands))
	endpoints := make([]string, len(commands))
	// Four server identities share one client ID in the package-wide Python
	// worker. Neither transport hooks nor role observations may route by that ID.
	for i := range refs {
		psk := Secret{byte(70 + i)}
		refs[i] = startReferenceWithOptions(t, cred, psk, false, map[string]any{"observe_controls": true})
		cred.Records = append(cred.Records, PairingRecord{ServerID: refs[i].id, PSK: psk})
	}
	for i := range refs {
		store := &MemoryStore{}
		if err = store.Save(context.Background(), cred); err != nil {
			t.Fatal(err)
		}
		clients[i], err = New(context.Background(), Config{Name: "Same-ID scoped peer", Store: store, Roles: []Role{Controller}, DisableDiscovery: true})
		if err != nil {
			t.Fatal(err)
		}
		endpoints[i], _ = serveTestClient(t, clients[i])
	}
	for i, ref := range refs {
		ref.control(t, map[string]any{"command": "connect", "url": endpoints[i]})
	}
	for i, ref := range refs {
		referenceArbitrationReady(t, clients[i], ref, false)
		ref.control(t, map[string]any{"command": "controller", "commands": []string{commands[i]}})
	}
	for i, client := range clients {
		eventually(t, func() bool {
			state := client.Snapshot()
			// The native controller always adds switch; no player is present to
			// add volume/mute. Exactly one peer-specific command must survive.
			return state.Controller != nil && len(state.Controller.SupportedCommands) == 2 && slices.Contains(state.Controller.SupportedCommands, "switch") && slices.Contains(state.Controller.SupportedCommands, commands[i])
		})
		if err = client.Command(context.Background(), Command{Command: commands[i]}); err != nil {
			t.Fatal(err)
		}
	}
	for i, ref := range refs {
		eventually(t, func() bool { return referenceEventCount(t, ref, "controller_command", "") == 1 })
		for _, raw := range ref.control(t, map[string]any{"command": "status"})["events"].([]any) {
			event := raw.(map[string]any)
			if event["type"] == "controller_command" && event["fields"].(map[string]any)["command"] != commands[i] {
				t.Fatal("controller observation was delivered to another same-ID peer")
			}
		}
		if state := clients[i].Snapshot(); state.ServerID != ref.id || !state.Available {
			t.Fatal("another same-ID peer interrupted the reference connection")
		}
	}
}

func TestE2EReferencePairingAttemptCannotBeDisplacedByPlayback(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{33}
	pairing := startReferenceWithOptions(t, cred, cred.PairingPSK, true, map[string]any{"hold_pairing": true})
	playback := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: playback.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Protected pairing", Store: store, Roles: []Role{Controller}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	pairing.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool {
		return pairing.control(t, map[string]any{"command": "status"})["pair_finalize_pending"] == true
	})
	playback.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return referenceEventCount(t, playback, "wire_goodbye", "concurrent_attempt") > 0 })
	eventually(t, func() bool { return referenceEventCount(t, playback, "disconnected", "") > 0 })
	if client.Snapshot().ServerID != pairing.id {
		t.Fatal("in-progress pairing displaced")
	}
	playback.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	pairing.control(t, map[string]any{"command": "release_pairing"})
	eventually(t, func() bool { return client.Snapshot().Available })
	playback.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == playback.id && client.Snapshot().Available })
}

func TestE2EReferenceIncomingPairingDoesNotInterruptPlayback(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{44}
	playback := startReference(t, cred, psk, false)
	pairing := startReference(t, cred, cred.PairingPSK, true)
	cred.Records = []PairingRecord{{ServerID: playback.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Playback holder", Store: store, Roles: []Role{Controller}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	playback.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == playback.id && client.Snapshot().Available })
	result := pairing.control(t, map[string]any{"command": "pair_at", "url": endpoint})
	if result["pair_abort"] != "concurrent_attempt" {
		t.Fatal("incoming pairing was not explicitly refused", result)
	}
	eventually(t, func() bool {
		if pairing.control(t, map[string]any{"command": "status"})["connected"] != false {
			return false
		}
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.sessions) == 1 && client.active != nil && client.active.channel.ServerID == playback.id
	})
	pairingFixtureBarrier(t, client, playback, "pairing-refusal-closed-and-released")
	if client.Snapshot().ServerID != playback.id {
		t.Fatal("pairing interrupted playback")
	}
	if pairing.control(t, map[string]any{"command": "status"})["pairing_record"] != false {
		t.Fatal("rejected pairing persisted a record")
	}
}

func TestE2EReferenceMisbindingCannotDisplaceAnAdmittedServer(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{45}
	trusted := startReference(t, cred, psk, false)
	// Same PSK, different static server identity: possession of a key must not
	// silently rebind the persisted pairing to another server_id.
	misbound := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: trusted.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Identity binding", Store: store, Roles: []Role{Controller}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	trusted.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == trusted.id && client.Snapshot().Available })
	misbound.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
wait:
	for {
		select {
		case <-timer.C:
			t.Fatal("misbound handshake was not rejected")
		case event := <-client.Events():
			if event.Kind == "disconnected" {
				if !errors.Is(event.Err, protocol.ErrProtocol) {
					t.Fatal("incorrect misbinding failure", event.Err)
				}
				break wait
			}
		}
	}
	misbound.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	if client.Snapshot().ServerID != trusted.id {
		t.Fatal("inadmissible candidate displaced current server")
	}
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0] != cred.Records[0] || got.LastPlaybackServer != trusted.id {
		t.Fatal("misbinding altered persistent trust")
	}
}

type handoffOutput struct {
	active  atomic.Int32
	opens   atomic.Int32
	closes  atomic.Int32
	overlap atomic.Bool
}
type handoffDevice struct {
	audio.Device
	owner *handoffOutput
}

func (o *handoffOutput) Open(ctx context.Context, format audio.Format) (audio.Device, error) {
	device, err := (clockOutput{}).Open(ctx, format)
	if err != nil {
		return nil, err
	}
	if o.active.Add(1) != 1 {
		o.overlap.Store(true)
	}
	o.opens.Add(1)
	return &handoffDevice{Device: device, owner: o}, nil
}
func (d *handoffDevice) Close() error {
	err := d.Device.Close()
	d.owner.active.Add(-1)
	d.owner.closes.Add(1)
	return err
}

func TestE2EReferencePlaybackHandoffClosesThePreviousOutput(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	pskA, pskB := Secret{46}, Secret{47}
	a := startReference(t, cred, pskA, false)
	b := startReference(t, cred, pskB, false)
	cred.Records = []PairingRecord{{ServerID: a.id, PSK: pskA}, {ServerID: b.id, PSK: pskB}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	output := &handoffOutput{}
	client, err := New(ctx, Config{Name: "Audio handoff", Store: store, Roles: []Role{Player}, Output: output, Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, stop := serveTestClient(t, client)
	a.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().Available })
	// Keep several seconds queued so displacement cancels an in-flight Write,
	// rather than merely closing an already empty output after playback ended.
	a.control(t, map[string]any{"command": "stream", "frames": 144000})
	if output.active.Load() != 1 {
		t.Fatal("first server did not open its output")
	}
	b.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == b.id && client.Snapshot().Available })
	if output.active.Load() != 0 || output.closes.Load() != 1 {
		t.Fatal("old output survived admission of the next server")
	}
	b.control(t, map[string]any{"command": "stream"})
	if output.overlap.Load() || output.active.Load() != 1 || output.opens.Load() != 2 {
		t.Fatal("two servers owned the output concurrently")
	}
	stop()
	if output.active.Load() != 0 || output.closes.Load() != 2 {
		t.Fatal("shutdown did not release the admitted output")
	}
}

func TestE2EReferenceProvisionalConnectionLimitPreservesTheHolder(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	pskA, pskB := Secret{48}, Secret{49}
	a := startReference(t, cred, pskA, false)
	b := startReference(t, cred, pskB, false)
	cred.Records = []PairingRecord{{ServerID: a.id, PSK: pskA}, {ServerID: b.id, PSK: pskB}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Connection capacity", Store: store, Roles: []Role{Controller}, DisableDiscovery: true, RecordCapacity: 5, HandshakeTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	a.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == a.id && client.Snapshot().Available })
	// Three WebSocket peers deliberately do not answer client/init. Together
	// with the admitted connection this exhausts the bounded provisional set.
	var stalled []*websocket.Conn
	for range 3 {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, _, err := websocket.Dial(dialCtx, endpoint, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		stalled = append(stalled, conn)
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.sessions) == 4 })
	b.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
wait:
	for {
		select {
		case <-timer.C:
			t.Fatal("over-capacity candidate was not rejected")
		case event := <-client.Events():
			if event.Kind == "disconnected" && errors.Is(event.Err, ErrConnectionLimit) {
				break wait
			}
		}
	}
	b.control(t, map[string]any{"command": "disconnect", "url": endpoint})
	if client.Snapshot().ServerID != a.id {
		t.Fatal("capacity exhaustion displaced the admitted connection")
	}
	for _, conn := range stalled {
		_ = conn.CloseNow()
	}
	eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.sessions) == 1 })
	b.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
	eventually(t, func() bool { return client.Snapshot().ServerID == b.id && client.Snapshot().Available })
}
