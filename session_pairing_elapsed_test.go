package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/monotime"
)

func TestPairingAttemptTimerExpiresByElapsedFirstMessageBudget(t *testing.T) {
	cfg := schemaConfig()
	cfg.Roles = []Role{Controller}
	s, p := newSchemaSession(t, cfg)
	// Keep the fixture's peer alive when canceling the actor: canceling their
	// shared parent context can race the actor's result with a peer-side EOF.
	s.ctx, s.cancel = context.WithCancel(s.ctx)
	t.Cleanup(s.cancel)
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, false))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{}})
	schemaRekeyCategory(t, s, p, "pr")
	before := monotime.Now()
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{"pairing"}, "active_roles": []Role{}, "pairing": map[string]any{"method": "pairing_psk"}})
	after := monotime.Now()
	if s.pairingDeadline.Before(before.Add(2*time.Minute)) || s.pairingDeadline.After(after.Add(2*time.Minute)) {
		t.Fatal("default attempt budget is not anchored to initial sends")
	}
	expectSchemaPayload(t, p, "client/pair-init", map[string]any{"pairing_index": float64(1)})
	if kind, _, err := p.readSecure(); err != nil || kind != "client/pair-finalize" {
		t.Fatal(err)
	}
	// Shorten only the test's already-started budget. Unlike an immediate
	// expiry injection, the actual actor ticker must wait and expire on elapsed
	// monotonic time. No remote finalize or callback/store blocking is involved.
	started := monotime.Now()
	s.pairingDeadline = started.Add(80 * time.Millisecond)
	done := make(chan error, 1)
	joined := false
	go func() { done <- s.run() }()
	defer func() {
		s.cancel()
		if !joined {
			<-done // Cleanup: retain the test failure while joining the canceled actor.
		}
	}()
	for {
		kind, raw, err := p.readSecure()
		if err != nil {
			t.Fatal(err)
		}
		if kind == "client/time" {
			continue
		}
		if kind != "pair/abort" {
			t.Fatal("unexpected attempt message", kind)
		}
		var abort struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(raw, &abort) != nil || abort.Reason != "attempt_timeout" {
			t.Fatal("wrong timer abort")
		}
		break
	}
	if elapsed := monotime.Since(started); elapsed < 80*time.Millisecond || elapsed > 2*time.Second {
		t.Fatal("attempt timer expired early or failed bounded cooperative execution", elapsed)
	}
	s.cancel()
	err := <-done
	joined = true
	if !errors.Is(err, context.Canceled) || s.pendingPSK != nil || s.pairingAttempt || !s.pairingDiscard {
		t.Fatal("expired actor retained attempt state", err)
	}
}
