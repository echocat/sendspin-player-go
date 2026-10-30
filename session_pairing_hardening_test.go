package sendspin

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

var errMissingPINBudget = errors.New("test: missing PIN attempt deadline")

type pinBudgetStore struct {
	Store
	expected    time.Time
	ignore      bool
	failure     error
	sawDeadline bool
}

func (s *pinBudgetStore) Save(ctx context.Context, c Credentials) error {
	deadline, ok := ctx.Deadline()
	s.sawDeadline = ok && deadline.Equal(s.expected)
	if !s.sawDeadline {
		return errMissingPINBudget
	}
	if s.failure != nil {
		return s.failure
	}
	if s.ignore {
		time.Sleep(max(0, time.Until(s.expected)) + time.Millisecond)
		return s.Store.Save(context.Background(), c)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestPINRoundStoreHonorsAttemptDeadlineBeforeDisplay(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(map[bool]string{false: "cooperative", true: "ignores-context"}[ignore], func(t *testing.T) {
			activeDisplays := 0
			s, p := newSchemaSession(t, Config{Name: "Bounded PIN round", Roles: []Role{}, CodePairing: &CodePairingConfig{Display: func(_ context.Context, e PairingDisplay) error {
				if e.Active {
					activeDisplays++
				}
				return nil
			}}})
			s.hello, s.activated = true, true
			s.channel.Category = "sn"
			key := Secret{7}
			s.pendingPSK = &key
			s.pairingDeadline = time.Now().Add(50 * time.Millisecond)
			s.codePairing = &codeAttempt{method: "dynamic_pairing_code", state: "init", h: s.channel.BindingHash(), nonceB: make([]byte, 32)}
			base := &MemoryStore{}
			if err := base.Save(context.Background(), s.client.credentials); err != nil {
				t.Fatal(err)
			}
			store := &pinBudgetStore{Store: base, expected: s.pairingDeadline, ignore: ignore}
			s.client.cfg.Store = store
			nonce := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			if err := s.codeMessage("server/pair-init", []byte(`{"nonce_A":"`+nonce+`"}`)); err != nil {
				t.Fatal("round counter did not expire through the attempt budget", err)
			}
			if !store.sawDeadline || activeDisplays != 0 || s.pendingPSK != nil || s.codePairing != nil || !s.pairingDiscard {
				t.Fatal("expired Store transaction reached PIN display or retained attempt state")
			}
			expectSchemaPayload(t, p, "pair/abort", map[string]any{"reason": "attempt_timeout"})
		})
	}
}

func TestPINCounterStoreOwnDeadlineDoesNotInventAttemptExpiry(t *testing.T) {
	s, _ := newSchemaSession(t, Config{Name: "PIN Store own error", Roles: []Role{}, CodePairing: &CodePairingConfig{Display: func(context.Context, PairingDisplay) error { return nil }}})
	s.hello, s.activated = true, true
	key := Secret{8}
	s.pendingPSK = &key
	s.pairingDeadline = time.Now().Add(time.Minute)
	s.codePairing = &codeAttempt{method: "dynamic_pairing_code", state: "init", h: s.channel.BindingHash(), nonceB: make([]byte, 32)}
	parentDeadline, _ := s.ctx.Deadline()
	store := &pinBudgetStore{Store: s.client.cfg.Store, expected: parentDeadline, failure: context.DeadlineExceeded}
	s.client.cfg.Store = store
	nonce := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if err := s.codeMessage("server/pair-init", []byte(`{"nonce_A":"`+nonce+`"}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Store-owned failure lost", err)
	}
	if !store.sawDeadline || s.pendingPSK == nil || s.codePairing == nil || s.pairingDiscard {
		t.Fatal("unexpired PIN attempt was confused with Store timeout")
	}
	s.endPairing(true)
}

func TestPairingSessionCannotCloseAnotherOrUnboundLocalWindow(t *testing.T) {
	c, err := New(context.Background(), Config{Name: "Window isolation", Store: &MemoryStore{}, Roles: []Role{}, CodePairing: &CodePairingConfig{Static: true}})
	if err != nil {
		t.Fatal(err)
	}
	owner := newSession(c, context.Background(), func() {})
	other := newSession(c, context.Background(), func() {})
	deadline := time.Now().Add(time.Minute)
	for _, bound := range []*session{nil, owner} {
		c.pairWindow = pairingWindow{expires: deadline, owner: bound}
		other.closePairingWindow()
		if c.pairWindow.expires != deadline || c.pairWindow.owner != bound {
			t.Fatal("unrelated peer canceled a local window")
		}
	}
	owner.closePairingWindow()
	if !c.pairWindow.expires.IsZero() {
		t.Fatal("owner did not close its window")
	}
}

func TestPairingWriteContextsShareAttemptBudgetButNotPendingOrAbort(t *testing.T) {
	c, err := New(context.Background(), Config{Name: "Pairing write budget", Store: &MemoryStore{}, Roles: []Role{}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(c, context.Background(), func() {})
	s.pairingDeadline = time.Now().Add(time.Second)
	for _, kind := range []string{"client/pair-init", "client/pair-auth", "client/pair-confirm", "client/pair-finalize", "client/pair-retry"} {
		ctx, cancel := s.applicationWriteContext(kind)
		deadline, ok := ctx.Deadline()
		cancel()
		if !ok || !deadline.Equal(s.pairingDeadline) {
			t.Fatal("pairing transmission can overrun attempt", kind)
		}
	}
	s.pairingDeadline = time.Now().Add(-time.Second)
	for _, kind := range []string{"client/pair-pending", "pair/abort", "client/time"} {
		ctx, cancel := s.applicationWriteContext(kind)
		if ctx.Err() != nil {
			cancel()
			t.Fatal("non-attempt/timeout notification inherited expired budget", kind)
		}
		cancel()
	}
	parent, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	s.ctx = parent
	s.pairingDeadline = time.Now().Add(time.Minute)
	ctx, cancel := s.applicationWriteContext("client/pair-finalize")
	defer cancel()
	want, _ := parent.Deadline()
	got, _ := ctx.Deadline()
	if !got.Equal(want) {
		t.Fatal("earlier parent budget extended")
	}
}
