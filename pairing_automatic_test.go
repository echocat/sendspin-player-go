package sendspin

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func automaticConfig(store Store) Config {
	return Config{Name: "Automatic PIN", Store: store, Roles: []Role{}, CodePairing: &CodePairingConfig{Automatic: true}}
}

func newAutomaticClient(t *testing.T, store Store) *Client {
	t.Helper()
	c, err := New(context.Background(), automaticConfig(store))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Drive the real encrypted session actor methods, without running its ticker.
// The schema fixture's original Store remains untouched for its own assertions.
func automaticSession(t *testing.T) (*session, *handshakePeer, *MemoryStore) {
	t.Helper()
	s, peer := newSchemaSession(t, automaticConfig(&MemoryStore{}))
	s.ctx, s.cancel = context.WithCancel(s.ctx)
	t.Cleanup(s.cancel)
	store := &MemoryStore{}
	s.client.cfg.Store = store
	s.client.credentials.Records = nil
	s.client.credentials.LastPlaybackServer = ""
	if err := store.Save(s.ctx, s.client.credentials); err != nil {
		t.Fatal(err)
	}
	s.channel.Category = "sn"
	s.hello, s.activated = true, true
	t.Cleanup(func() { s.endPairing(true) })
	return s, peer, store
}

func startAutomaticAttempt(t *testing.T, s *session, p *handshakePeer) {
	t.Helper()
	if err := s.activate([]byte(`{"activities":["pairing"],"active_roles":[],"pairing":{"method":"dynamic_pairing_code","format":"digits"}}`)); err != nil {
		t.Fatal(err)
	}
	readAutomaticInit(t, p)
}

func readAutomaticInit(t *testing.T, p *handshakePeer) {
	t.Helper()
	kind, raw, err := p.readSecure()
	var msg struct {
		Commit string `json:"commit_B"`
	}
	if err != nil || kind != "client/pair-init" || json.Unmarshal(raw, &msg) != nil || len(msg.Commit) != 43 {
		t.Fatal("missing standard dynamic init", kind, err)
	}
}

func automaticRound(t *testing.T, s *session, first bool) PairingPINDisplay {
	t.Helper()
	message := `{}`
	if first {
		message = `{"nonce_A":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 32)) + `"}`
	}
	if err := s.codeMessage("server/pair-init", []byte(message)); err != nil {
		t.Fatal(err)
	}
	pin, ok := s.client.CurrentPairingPIN()
	if !ok || len(pin.PIN.Value()) != 6 || !pin.ValidUntil.Equal(s.pairingDeadline) {
		t.Fatal("missing six-digit, attempt-bounded display")
	}
	return pin
}

func automaticAuth(t *testing.T, s *session, p *handshakePeer) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Valid CPace input point but no matching password transcript. The all-zero
	// key confirmation below is a cryptographically invalid proof, not a drop.
	msg := `{"pake_msg_1":"` + base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) + `"}`
	if err := s.codeMessage("server/pair-auth", []byte(msg)); err != nil {
		t.Fatal(err)
	}
	kind, _, err := p.readSecure()
	if err != nil || kind != "client/pair-auth" {
		t.Fatal("missing PAKE response", kind, err)
	}
}

func automaticMismatch(t *testing.T, s *session, p *handshakePeer, abort bool) {
	t.Helper()
	automaticAuth(t, s, p)
	msg := `{"server_kc":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 64)) + `"}`
	if err := s.codeMessage("server/pair-confirm", []byte(msg)); err != nil {
		t.Fatal(err)
	}
	if abort {
		expectSchemaPayload(t, p, "pair/abort", map[string]any{"reason": "pairing_code_mismatch"})
	} else {
		expectSchemaPayload(t, p, "client/pair-retry", map[string]any{})
	}
}

func TestAutomaticPairingStartupRestartAndMigration(t *testing.T) {
	ctx := context.Background()
	store := FileStore{Path: filepath.Join(t.TempDir(), "credentials.json")}
	legacy, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	legacy.Volume, legacy.Muted, legacy.OutputDelayMS = 23, true, 71
	legacy.StaticPairingPIN = &Secret{'0', '1', '2', '3', '4', '5', '6', '7'}
	if err := store.Save(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		c := newAutomaticClient(t, store)
		state := c.PairingState()
		if !state.Automatic || !state.Ready || state.Paired || state.Active || state.RequiresOperator {
			t.Fatal("unpaired boot requires an operator", state)
		}
		if _, ok := c.CurrentPairingPIN(); ok {
			t.Fatal("PIN exists before the server nonce")
		}
		stored, _ := store.Load(ctx)
		if !reflect.DeepEqual(stored, legacy) {
			t.Fatal("opt-in migration changed identity, settings or credentials")
		}
	}
	legacy.Records = []PairingRecord{{ServerID: legacy.ClientID(), PSK: Secret{5}}}
	if err := store.Save(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	c := newAutomaticClient(t, store)
	if p := c.PairingState(); !p.Paired || p.Ready {
		t.Fatal("offline record reopened automatic pairing", p)
	}
	if stored, err := store.Load(ctx); err != nil || !reflect.DeepEqual(stored, legacy) {
		t.Fatal("paired migration rewrote or removed existing credentials", err)
	}
	if _, err := New(ctx, Config{Name: "Invalid", Store: store, Roles: []Role{}, CodePairing: &CodePairingConfig{Static: true, Automatic: true}}); err == nil {
		t.Fatal("static automatic pairing accepted")
	}
}

func TestAutomaticPairingFiveFailuresCooldownAndReboot(t *testing.T) {
	s, peer, store := automaticSession(t)
	startAutomaticAttempt(t, s, peer)
	var first PairingPINDisplay
	for i := range 5 {
		pin := automaticRound(t, s, i == 0)
		if i == 0 {
			first = pin
		} else if first.PIN.Value() != pin.PIN.Value() {
			t.Fatal("retry changed the protocol's bound PIN")
		}
		automaticMismatch(t, s, peer, i == 4)
		stored, _ := store.Load(s.ctx)
		if stored.DynamicPairingRounds != uint32(i+1) || stored.PairingFailures != uint8((i+1)%5) || stored.PairingAuthPending {
			t.Fatal("incorrect durable accounting")
		}
	}
	if _, ok := s.client.CurrentPairingPIN(); ok {
		t.Fatal("fifth failure still exposes old PIN")
	}
	state := s.client.PairingState()
	if state.Ready || state.Active || state.CooldownRemaining < 29*time.Second {
		t.Fatal("missing device-wide cooldown", state)
	}
	reboot := newAutomaticClient(t, store)
	if p := reboot.PairingState(); p.Ready || p.Rounds != 5 || p.CooldownRemaining < 29*time.Second {
		t.Fatal("reboot bypassed rate protection", p)
	}
	// Expire the process-local monotonic deadline, not the wall clock or disk.
	s.client.mu.Lock()
	s.client.pairingCooldownUntil = pairingNow().Add(-time.Second)
	s.client.mu.Unlock()
	if p := s.client.PairingState(); !p.Ready || p.Rounds != 5 {
		t.Fatal("cooldown release reset the round limit", p)
	}
	startAutomaticAttempt(t, s, peer)
	if _, ok := s.client.CurrentPairingPIN(); ok {
		t.Fatal("new attempt exposes the old PIN before nonce exchange")
	}
	_ = automaticRound(t, s, true)
	stored, _ := store.Load(s.ctx)
	if stored.PairingCooldown || stored.DynamicPairingRounds != 6 {
		t.Fatal("resumption did not atomically retain the round budget")
	}
	// A restart even after apparent elapsed downtime conservatively waits again
	// until the clearing transaction; there is no wall clock to wind forward.
}

func TestAutomaticPairingCounterPersistenceErrorsFailClosed(t *testing.T) {
	for _, phase := range []string{"round", "auth", "mismatch"} {
		t.Run(phase, func(t *testing.T) {
			s, peer, store := automaticSession(t)
			startAutomaticAttempt(t, s, peer)
			if phase != "round" {
				_ = automaticRound(t, s, true)
			}
			if phase == "mismatch" {
				automaticAuth(t, s, peer)
			}
			failure := errors.New("controlled counter write failure")
			s.client.cfg.Store = automaticFaultStore{Store: store, save: func(context.Context, Credentials) error { return failure }}
			var err error
			switch phase {
			case "round":
				err = s.codeMessage("server/pair-init", []byte(`{"nonce_A":"`+base64.RawURLEncoding.EncodeToString(make([]byte, 32))+`"}`))
			case "auth":
				key, keyErr := ecdh.X25519().GenerateKey(rand.Reader)
				if keyErr != nil {
					t.Fatal(keyErr)
				}
				err = s.codeMessage("server/pair-auth", []byte(`{"pake_msg_1":"`+base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())+`"}`))
			case "mismatch":
				err = s.codeMessage("server/pair-confirm", []byte(`{"server_kc":"`+base64.RawURLEncoding.EncodeToString(make([]byte, 64))+`"}`))
			}
			if !errors.Is(err, failure) || !s.client.PairingState().StorageBlocked {
				t.Fatal("Store failure was hidden or automatic pairing stayed open", err)
			}
			if _, ok := s.client.CurrentPairingPIN(); ok {
				t.Fatal("PIN still offered after protection persistence failure")
			}
			s.endPairing(true)
			if s.client.PairingState().Ready {
				t.Fatal("teardown reset storage fault latch")
			}
			if phase == "mismatch" {
				reboot := newAutomaticClient(t, store)
				if p := reboot.PairingState(); p.Ready || p.CooldownRemaining < 29*time.Second || p.Rounds != 1 {
					t.Fatal("failed mismatch write lost durable reservation", p)
				}
			}
		})
	}
}

func TestAutomaticPairingPauseAndOperatorUnlockPreserveProtection(t *testing.T) {
	store := &MemoryStore{}
	c := newAutomaticClient(t, store)
	if err := c.update(context.Background(), func(next *Credentials) error {
		next.DynamicPairingRounds, next.PairingFailures, next.PairingCooldown = 7, 4, true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.pairingCooldownUntil = pairingNow().Add(pairingCooldown)
	if err := c.OpenPairingWindow(context.Background()); err != nil || c.PairingState().Rounds != 7 {
		t.Fatal("repeated non-unlocking gesture reset active round budget", err)
	}
	if err := c.CancelPairing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := c.PairingState(); !p.Paused || p.Ready {
		t.Fatal("cancel failed to pause readiness", p)
	}
	if err := c.OpenPairingWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := c.PairingState(); p.Paused || p.Ready || p.FailedConfirmations != 4 || p.CooldownRemaining < 29*time.Second {
		t.Fatal("operator resume bypassed short-term protection", p)
	}
	if result, err := c.RestartPairing(context.Background()); err != nil || !result.Committed {
		t.Fatal(err)
	}
	if p := c.PairingState(); p.Ready || p.FailedConfirmations != 4 || p.CooldownRemaining < 29*time.Second {
		t.Fatal("administrator reset bypassed short-term protection", p)
	}
}

func TestAutomaticPairingUnknownOutcomeIsNotWrongPIN(t *testing.T) {
	s, peer, store := automaticSession(t)
	startAutomaticAttempt(t, s, peer)
	_ = automaticRound(t, s, true)
	automaticAuth(t, s, peer)
	stored, _ := store.Load(s.ctx)
	if !stored.PairingAuthPending || stored.PairingFailures != 0 {
		t.Fatal("response was emitted without a durable reservation")
	}
	s.endPairing(true) // Network loss, timeout or cancellation; no false proof.
	if p := s.client.PairingState(); p.FailedConfirmations != 0 || p.Ready || p.CooldownRemaining < 29*time.Second {
		t.Fatal("unknown outcome either counted as wrong or bypassed protection", p)
	}
	reboot := newAutomaticClient(t, store)
	if p := reboot.PairingState(); p.FailedConfirmations != 0 || p.Rounds != 1 || p.CooldownRemaining < 29*time.Second {
		t.Fatal("crash reservation lost", p)
	}
}

func TestAutomaticPairingPartialFailureBudgetSurvivesNewClient(t *testing.T) {
	s, peer, store := automaticSession(t)
	startAutomaticAttempt(t, s, peer)
	for i := range 4 {
		_ = automaticRound(t, s, i == 0)
		automaticMismatch(t, s, peer, false)
	}
	s.endPairing(true)
	restarted := newAutomaticClient(t, store)
	if p := restarted.PairingState(); !p.Ready || p.FailedConfirmations != 4 || p.Rounds != 4 {
		t.Fatal("new process reset a partial failure budget", p)
	}
	if err := restarted.OpenPairingWindow(s.ctx); err != nil || restarted.PairingState().Rounds != 4 {
		t.Fatal("non-unlocking gesture bypassed persisted rounds", err)
	}
	// Reuse only the encrypted fixture transport; the policy/credentials belong
	// to a newly constructed Client, with no previous connection-local counters.
	s.client = restarted
	restarted.sessions[s], restarted.active = true, s
	startAutomaticAttempt(t, s, peer)
	_ = automaticRound(t, s, true)
	automaticMismatch(t, s, peer, true)
	if p := restarted.PairingState(); p.Ready || p.CooldownRemaining < 29*time.Second || p.Rounds != 5 {
		t.Fatal("fifth failure in a new Client escaped cooldown", p)
	}
}

func TestAutomaticPairingTwentyRoundLimitSurvivesCooldownAndRestart(t *testing.T) {
	s, peer, store := automaticSession(t)
	for batch := range 4 {
		startAutomaticAttempt(t, s, peer)
		for i := range 5 {
			_ = automaticRound(t, s, i == 0)
			automaticMismatch(t, s, peer, i == 4)
		}
		s.client.mu.Lock()
		s.client.pairingCooldownUntil = pairingNow().Add(-time.Second)
		s.client.mu.Unlock()
		if got := s.client.PairingState().Rounds; got != uint32((batch+1)*5) {
			t.Fatal("rotation reset global rounds")
		}
	}
	for _, c := range []*Client{s.client, newAutomaticClient(t, store)} {
		if p := c.PairingState(); !p.RequiresOperator || p.Ready || p.Rounds != 20 {
			t.Fatal("twenty-round lockout bypassed", p)
		}
	}
	if err := s.client.OpenPairingWindow(s.ctx); err != nil {
		t.Fatal(err)
	}
	if p := s.client.PairingState(); !p.Ready || p.RequiresOperator || p.Rounds != 0 {
		t.Fatal("deliberate operator unlock failed", p)
	}
}

func TestAutomaticPairingPINSecretExpiryAndDurableSuccess(t *testing.T) {
	s, peer, store := automaticSession(t)
	startAutomaticAttempt(t, s, peer)
	pin := automaticRound(t, s, true)
	for _, object := range []any{pin, pin.PIN, s.client.PairingState(), s.client.Snapshot()} {
		text := fmt.Sprintf("%+v %#v", object, object)
		data, _ := json.Marshal(object)
		if strings.Contains(text, pin.PIN.Value()) || strings.Contains(string(data), pin.PIN.Value()) {
			t.Fatal("PIN leaked through non-explicit formatting/JSON")
		}
	}
	oldRevision := pin.Revision
	s.pairingDeadline = pairingNow().Add(-time.Second)
	s.client.mu.Lock()
	s.client.pairingPIN.ValidUntil = s.pairingDeadline
	s.client.mu.Unlock()
	if _, ok := s.client.CurrentPairingPIN(); ok {
		t.Fatal("expired PIN exposed between actor ticks")
	}
	if expired, err := s.checkPairingTimeout(pairingNow()); !expired || err != nil {
		t.Fatal("attempt did not expire", err)
	}
	expectSchemaPayload(t, peer, "pair/abort", map[string]any{"reason": "attempt_timeout"})
	if err := s.dispatch(append([]byte{0}, peerJSON("server/pair-finalize", map[string]any{})...)); err != nil {
		t.Fatal("old in-flight finalize not silently discarded", err)
	}
	startAutomaticAttempt(t, s, peer)
	_ = automaticRound(t, s, true)
	if err := s.finalizePairing([]byte(`{}`)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("old finalize completed a new unconfirmed attempt", err)
	}
	// Persistence/transition test, not a cryptographic success test. Independent
	// reference-peer E2E tests exercise the actual PAKE confirmation and rekey.
	s.codePairing.state = "finalize"
	if err := s.finalizePairing([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.client.CurrentPairingPIN(); ok {
		t.Fatal("PIN exposed after durable pairing")
	}
	if p := s.client.PairingState(); !p.Paired || p.Ready || p.Active || p.Revision <= oldRevision {
		t.Fatal("pairing transition missing", p)
	}
	reboot := newAutomaticClient(t, store)
	if p := reboot.PairingState(); !p.Paired || p.Ready {
		t.Fatal("offline paired reboot became unpaired", p)
	}
}

type automaticFaultStore struct {
	Store
	save func(context.Context, Credentials) error
}

func (s automaticFaultStore) Save(ctx context.Context, c Credentials) error {
	return s.save(ctx, c)
}

func TestPairingRestartPreservesIdentityAndSettingsRevokesAllCredentials(t *testing.T) {
	store := &MemoryStore{}
	c := newAutomaticClient(t, store)
	old := c.credentials
	old.Volume, old.Muted, old.OutputDelayMS = 17, true, 98
	pin := Secret{'1', '2', '3', '4', '5', '6', '7', '8'}
	old.StaticPairingPIN = &pin
	old.Records = []PairingRecord{{ServerID: old.ClientID(), PSK: Secret{7}}}
	old.LastPlaybackServer = old.ClientID()
	old.DynamicPairingRounds, old.PairingFailures = 20, 4
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	c = newAutomaticClient(t, store)
	token := c.PairingToken()
	result, err := c.RestartPairing(context.Background())
	if err != nil || !result.Committed {
		t.Fatal("reset did not commit", err)
	}
	next, _ := store.Load(context.Background())
	if next.PrivateKey != old.PrivateKey || next.ClientID() != old.ClientID() || next.Volume != 17 || !next.Muted || next.OutputDelayMS != 98 || next.PairingFailures != 4 {
		t.Fatal("reset changed identity, player settings or short-term protection")
	}
	if len(next.Records) != 0 || next.PairingPSK == old.PairingPSK || next.StaticPairingPIN != nil || next.LastPlaybackServer != "" || next.DynamicPairingRounds != 0 || c.PairingToken() == token {
		t.Fatal("old pairing credentials survived")
	}
	for _, credential := range []struct {
		category string
		psk      Secret
	}{{"pr", old.PairingPSK}, {"lt", old.Records[0].PSK}} {
		selection, err := c.resolver(old.ClientID())(pskID(credential.psk), credential.category, true)
		if err != nil || selection.Category != "sn" {
			t.Fatal("old credential did not fall back to unauthenticated Sentinel", err)
		}
		if _, err := c.resolver(old.ClientID())(pskID(credential.psk), credential.category, false); err == nil {
			t.Fatal("old re-handshake credential accepted")
		}
	}
	if p := c.PairingState(); !p.Ready || p.Paired || p.Restarting {
		t.Fatal("reset did not start automatic readiness", p)
	}
}

func TestPairingRestartPreservesHostAudioBindingAndObservedState(t *testing.T) {
	cfg := automaticConfig(&MemoryStore{})
	cfg.PlayerControl = &PlayerControlConfig{}
	c, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	volume, muted := 42, true
	report := PlayerControlReport{Volume: &volume, Muted: &muted}
	binding, err := c.BindPlayerControl(PlayerControlTarget{Initial: report})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RestartPairing(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := c.Snapshot()
	if !state.VolumeKnown || state.Volume != volume || !state.MutedKnown || !state.Muted {
		t.Fatal("pairing reset changed observed host audio state")
	}
	next, err := c.ReportPlayerControl(binding, report)
	if err != nil || next != binding {
		t.Fatal("pairing reset invalidated unrelated host target binding", err)
	}
}

func TestAutomaticPairingOnlyOneActiveAttempt(t *testing.T) {
	c := newAutomaticClient(t, &MemoryStore{})
	first := newSession(c, context.Background(), func() {})
	second := newSession(c, context.Background(), func() {})
	c.sessions[first], c.sessions[second] = true, true
	first.codePairing = &codeAttempt{method: "dynamic_pairing_code"}
	second.codePairing = &codeAttempt{method: "dynamic_pairing_code"}
	if !first.beginPairingAttempt() || second.beginPairingAttempt() {
		t.Fatal("device admitted concurrent PIN attempts")
	}
	first.setPairingAttempt(false)
	if !second.beginPairingAttempt() {
		t.Fatal("completed first attempt did not release device")
	}
}

func TestPairingRestartFailureAndCancellationCommitSemantics(t *testing.T) {
	for _, phase := range []string{"save-failure", "canceled-before", "canceled-after-commit"} {
		t.Run(phase, func(t *testing.T) {
			store := &MemoryStore{}
			c := newAutomaticClient(t, store)
			old, _ := store.Load(context.Background())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("controlled persistence failure")
			c.cfg.Store = automaticFaultStore{Store: store, save: func(ctx context.Context, next Credentials) error {
				if phase == "save-failure" {
					return failure
				}
				if err := store.Save(ctx, next); err != nil {
					return err
				}
				cancel() // Store committed; canceled context is not rollback.
				return nil
			}}
			if phase == "canceled-before" {
				cancel()
			}
			result, err := c.RestartPairing(ctx)
			if err == nil {
				t.Fatal("failure/cancellation reported success")
			}
			next, _ := store.Load(context.Background())
			if phase == "canceled-after-commit" {
				if !result.Committed || !errors.Is(err, context.Canceled) || next.PairingPSK == old.PairingPSK || next.PairingPSK != c.credentials.PairingPSK {
					t.Fatal("post-commit cancellation falsely rolled back", err)
				}
			} else if result.Committed || !reflect.DeepEqual(next, old) || !reflect.DeepEqual(c.credentials, old) || c.pairingGeneration != 0 {
				t.Fatal("failed reset changed live or durable credentials")
			}
		})
	}
}

func TestPairingRestartBlocksUntilOldActorsExitAndRejectsLateWrites(t *testing.T) {
	c := newAutomaticClient(t, &MemoryStore{})
	sctx, scancel := context.WithCancel(context.Background())
	s := newSession(c, sctx, scancel)
	s.channel = &wire.Channel{ServerID: c.ClientID(), Category: "pr"}
	s.pendingPSK = &Secret{9}
	s.pairingDeadline = pairingNow().Add(time.Minute)
	c.sessions[s], c.active = true, s
	ctx, cancel := context.WithCancel(context.Background())
	store := c.cfg.Store
	c.cfg.Store = automaticFaultStore{Store: store, save: func(ctx context.Context, next Credentials) error {
		if err := store.Save(ctx, next); err != nil {
			return err
		}
		cancel()
		return nil
	}}
	result, err := c.RestartPairing(ctx)
	if !result.Committed || !errors.Is(err, context.Canceled) || sctx.Err() == nil {
		t.Fatal("post-commit revocation did not run", err)
	}
	if p := c.PairingState(); !p.Restarting || p.Ready || c.active != nil {
		t.Fatal("new pairing opened while old actor still running", p)
	}
	if _, err := c.sessionResolver(s, s.channel.ServerID)(pskID(c.credentials.PairingPSK), "pr", true); !errors.Is(err, ErrPairingRestarting) {
		t.Fatal("old handshake resolved new credentials", err)
	}
	if err := s.update(context.Background(), func(next *Credentials) error {
		next.Records = append(next.Records, PairingRecord{ServerID: s.channel.ServerID, PSK: Secret{8}})
		return nil
	}); !errors.Is(err, ErrPairingRestarting) {
		t.Fatal("late callback restored trust", err)
	}
	if err := s.finalizePairing([]byte(`{}`)); err == nil {
		t.Fatal("old finalize accepted")
	}
	c.mu.Lock()
	delete(c.sessions, s)
	close(s.done)
	c.mu.Unlock()
	if p := c.PairingState(); !p.Ready || p.Restarting {
		t.Fatal("cleanup did not release readiness", p)
	}
}

func TestAutomaticPairingConcurrentFinalizeResetAndStateReads(t *testing.T) {
	for range 30 {
		store := &MemoryStore{}
		c := newAutomaticClient(t, store)
		sctx, cancel := context.WithCancel(context.Background())
		s := newSession(c, sctx, cancel)
		s.channel = &wire.Channel{ServerID: c.ClientID(), Category: "pr"}
		s.pendingPSK = &Secret{9}
		s.pairingDeadline = pairingNow().Add(time.Minute)
		// Not registered: this fixture tests transaction linearization, not
		// transport cleanup (covered above and by reference-peer E2E).
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); _ = s.finalizePairing([]byte(`{}`)) }()
		go func() { defer wg.Done(); _, _ = c.RestartPairing(context.Background()) }()
		go func() {
			defer wg.Done()
			for range 20 {
				_ = c.PairingState()
				_, _ = c.CurrentPairingPIN()
				_ = c.Snapshot()
			}
		}()
		wg.Wait()
		cancel()
		next, _ := store.Load(context.Background())
		if len(next.Records) != 0 || len(c.credentials.Records) != 0 {
			t.Fatal("late finalize resurrected records after reset")
		}
	}
}
