//go:build e2e

package sendspin

import (
	"context"
	"crypto/ecdh"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
	"github.com/flynn/noise"
)

func TestReferencePairingDeadlineStopsFinalizeAfterFirstWrite(t *testing.T) {
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	reference := startReferenceWithOptions(t, credentials, credentials.PairingPSK, true, map[string]any{"hold_pairing": true, "observe_pairing": true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := &MemoryStore{}
	if err = store.Save(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int32
	var originalRemaining atomic.Int64
	var client *Client
	client, err = New(ctx, Config{Name: "Pairing timeout boundary", Store: store, Roles: []Role{Controller}, DisableDiscovery: true, Observe: func(message diagnostic.Message) {
		if message.Direction != diagnostic.Sent || !message.Encrypted || message.Type != "redacted/control" || sent.Add(1) != 1 {
			return
		}
		// The first encrypted redacted send is the native flow's pair-init.
		// This callback runs on the session actor AFTER transmission but BEFORE
		// its Write returns. Advance only that actor-owned test deadline, not
		// the wall clock or the independent server's pairing state machine.
		client.mu.Lock()
		s := client.active
		client.mu.Unlock()
		originalRemaining.Store(int64(s.pairingDeadline.Sub(message.At)))
		s.pairingDeadline = message.At.Add(-time.Nanosecond)
	}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := serveTestClient(t, client)
	reference.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool {
		return referenceEventCount(t, reference, "pair_abort", "attempt_timeout") > 0 || referenceEventCount(t, reference, "pair_finalize", "") > 0
	})
	if referenceEventCount(t, reference, "pair_finalize", "") != 0 {
		t.Fatal("expired attempt disclosed a long-term PSK after pair-init Write returned")
	}
	if referenceEventCount(t, reference, "pair_abort", "attempt_timeout") != 1 {
		t.Fatal("expired attempt did not send exactly one attempt_timeout abort")
	}
	remaining := time.Duration(originalRemaining.Load())
	if remaining <= 0 || remaining > 2*time.Minute {
		t.Fatal("attempt deadline did not start with its first send", remaining)
	}
	if persisted, err := store.Load(ctx); err != nil || len(persisted.Records) != 0 {
		t.Fatal("expired attempt persisted a record", err)
	}
	if reference.control(t, map[string]any{"command": "status"})["pairing_record"] != false {
		t.Fatal("reference persisted a timed-out attempt")
	}
	// The native abort leaves the connection usable. A new native attempt still
	// needs the next activation, gets the next index, and can persist normally.
	pairingFixtureBarrier(t, client, reference, "after-pairing-timeout")
	reference.control(t, map[string]any{"command": "release_pairing"})
	reference.control(t, map[string]any{"command": "pair"})
	eventually(t, func() bool { return client.Snapshot().Available })
	persisted, err := store.Load(ctx)
	if err != nil || len(persisted.Records) != 1 || persisted.Records[0].ServerID != reference.id {
		t.Fatal("native retry did not persist a bound record", err)
	}
	requirePairInitIndex(t, reference, 1, 2)
}

func TestReferenceExpiredPairingFinalizeCannotBeatTheActorTimer(t *testing.T) {
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	reference := startReferenceWithOptions(t, credentials, credentials.PairingPSK, true, map[string]any{"observe_pairing": true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unrelated, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	previousRecord := PairingRecord{ServerID: unrelated.ClientID(), PSK: Secret{104}}
	credentials.Records = []PairingRecord{previousRecord}
	store := &MemoryStore{}
	if err = store.Save(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Finalize before timer selection", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(reference.url, "http")+"/sendspin", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	s := newSession(client, ctx, cancel)
	client.sessions[s] = true
	private, err := ecdh.X25519().NewPrivateKey(credentials.PrivateKey[:])
	if err != nil {
		t.Fatal(err)
	}
	s.channel, err = wire.Handshake(ctx, conn, noise.DHKey{Private: credentials.PrivateKey[:], Public: private.PublicKey().Bytes()}, client.cfg.CipherSuite, client.cfg.MaxMessageBytes, func(serverID string) wire.Resolve { return client.sessionResolver(s, serverID) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Manually drive the production dispatcher against the native reference.
	// No actor loop/ticker exists, so timeout cannot win by timer selection.
	// The peer's actual encrypted finalize is received, not synthesized here.
	for {
		frame, err := s.channel.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(frame) == 0 || frame[0] != 0 {
			t.Fatal("unexpected binary pairing message")
		}
		message, err := protocol.Parse(frame[1:])
		if err != nil {
			t.Fatal(err)
		}
		if message.Type != "server/pair-finalize" {
			if err = s.dispatch(frame); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if s.pendingPSK == nil || !s.pairingAttempt {
			t.Fatal("native attempt never reached finalize")
		}
		s.pairingDeadline = time.Now().Add(-time.Nanosecond)
		if err = s.dispatch(frame); err != nil {
			t.Fatal("timeout closed instead of aborting the attempt", err)
		}
		break
	}
	if s.pendingPSK != nil || s.pairingAttempt || !s.pairingDeadline.IsZero() || !s.pairingDiscard {
		t.Fatal("late finalize did not end the expired attempt")
	}
	persisted, err := store.Load(ctx)
	if err != nil || len(persisted.Records) != 1 || persisted.Records[0] != previousRecord {
		t.Fatal("late finalize persisted a key or changed an unrelated record", err)
	}
	if reference.control(t, map[string]any{"command": "status"})["pairing_record"] != true {
		t.Fatal("test did not receive a native post-persistence finalize")
	}
	// The peer may already have committed. Local expiry must neither accept
	// that late key nor delete unrelated records; later repair uses Sentinel.
}

func TestReferencePairingStoreDeadlineAbortsWithoutActorTimer(t *testing.T) {
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	reference := startReferenceWithOptions(t, credentials, credentials.PairingPSK, true, map[string]any{"observe_pairing": true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := &pairingDeadlineStore{}
	if err = store.Save(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Cooperative Store attempt deadline", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(reference.url, "http")+"/sendspin", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	s := newSession(client, ctx, cancel)
	client.sessions[s] = true
	private, err := ecdh.X25519().NewPrivateKey(credentials.PrivateKey[:])
	if err != nil {
		t.Fatal(err)
	}
	s.channel, err = wire.Handshake(ctx, conn, noise.DHKey{Private: credentials.PrivateKey[:], Public: private.PublicKey().Bytes()}, client.cfg.CipherSuite, client.cfg.MaxMessageBytes, func(serverID string) wire.Resolve { return client.sessionResolver(s, serverID) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The real peer commits and sends its finalize. Only then start a short
	// future deadline and a context-cooperative local Save; there is no actor
	// ticker or synthetic remote finalize to rescue an unbounded Store call.
	for {
		frame, err := s.channel.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(frame) == 0 || frame[0] != 0 {
			t.Fatal("unexpected binary pairing data")
		}
		message, err := protocol.Parse(frame[1:])
		if err != nil {
			t.Fatal(err)
		}
		if message.Type == "server/pair-finalize" {
			s.pairingDeadline = time.Now().Add(500 * time.Millisecond)
			store.expected = s.pairingDeadline
			if err = s.dispatch(frame); err != nil {
				t.Fatal("Store attempt deadline did not produce a non-closing timeout abort", err)
			}
			break
		}
		if err = s.dispatch(frame); err != nil {
			t.Fatal(err)
		}
	}
	if !store.sawDeadline || ctx.Err() != nil || s.pendingPSK != nil || s.pairingAttempt || !s.pairingDiscard {
		t.Fatal("Store timeout failed to clear only the expired attempt")
	}
	stored, err := store.Load(ctx)
	if err != nil || len(stored.Records) != 0 || len(client.credentials.Records) != 0 {
		t.Fatal("timed-out cooperative Store changed local trust", err)
	}
	eventually(t, func() bool { return referenceEventCount(t, reference, "pair_abort", "attempt_timeout") == 1 })
	if reference.control(t, map[string]any{"command": "status"})["pairing_record"] != true {
		t.Fatal("test did not preserve the real peer's prior commit")
	}
}
