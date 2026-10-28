package sendspin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/wire"
)

type pairingDeadlineStore struct {
	MemoryStore
	expected    time.Time
	sawDeadline bool
	failure     error
}

func (s *pairingDeadlineStore) Save(ctx context.Context, c Credentials) error {
	if !s.expected.IsZero() {
		deadline, ok := ctx.Deadline()
		s.sawDeadline = ok && deadline.Equal(s.expected)
		if s.failure == nil {
			<-ctx.Done()
			return ctx.Err()
		}
		return s.failure
	}
	return s.MemoryStore.Save(ctx, c)
}

func TestStoreOwnTimeoutDoesNotInventPairingExpiry(t *testing.T) {
	store := &pairingDeadlineStore{failure: context.DeadlineExceeded}
	client, err := New(context.Background(), Config{Name: "Store timeout is not attempt timeout", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, context.Background(), func() {})
	s.channel = &wire.Channel{ServerID: client.ClientID(), Category: "pr"}
	// No socket/cipher states: an invented attempt_timeout write would panic.
	key := Secret{129}
	s.pendingPSK = &key
	s.pairingDeadline = time.Now().Add(time.Minute)
	store.expected = s.pairingDeadline
	s.setPairingAttempt(true)
	if err = s.finalizePairing([]byte(`{}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Store-owned deadline was swallowed", err)
	}
	if s.pendingPSK == nil || !s.pairingAttempt || s.pairingDiscard {
		t.Fatal("unexpired attempt was incorrectly treated as expired")
	}
}

func TestPairingPersistencePassesAttemptDeadlineToStore(t *testing.T) {
	failure := errors.New("controlled store refusal")
	store := &pairingDeadlineStore{failure: failure}
	client, err := New(context.Background(), Config{Name: "Pairing store deadline", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, context.Background(), func() {})
	s.channel = &wire.Channel{ServerID: server.ClientID(), Category: "pr"}
	key := Secret{128}
	s.pendingPSK = &key
	s.pairingDeadline = time.Now().Add(time.Minute)
	store.expected = s.pairingDeadline
	if err = s.finalizePairing([]byte(`{}`)); !errors.Is(err, failure) {
		t.Fatal("Store error lost", err)
	}
	if !store.sawDeadline {
		t.Fatal("context-cooperative Store received no attempt deadline")
	}
	stored, err := store.Load(context.Background())
	if err != nil || len(stored.Records) != 0 || len(client.credentials.Records) != 0 {
		t.Fatal("failed persistence changed trust", err)
	}
}

func TestParentStoreDeadlineDoesNotInventAttemptTimeout(t *testing.T) {
	store := &pairingDeadlineStore{}
	client, err := New(context.Background(), Config{Name: "Earlier parent deadline", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	s := newSession(client, ctx, func() {})
	s.channel = &wire.Channel{ServerID: client.ClientID(), Category: "pr"}
	key := Secret{130}
	s.pendingPSK = &key
	s.pairingDeadline = time.Now().Add(time.Minute)
	s.setPairingAttempt(true)
	store.expected, _ = ctx.Deadline()
	if err = s.finalizePairing([]byte(`{}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("parent deadline was swallowed", err)
	}
	if !store.sawDeadline || s.pendingPSK == nil || !s.pairingAttempt || s.pairingDiscard {
		t.Fatal("parent cancellation was mistaken for attempt expiry")
	}
	if len(client.credentials.Records) != 0 {
		t.Fatal("parent deadline committed local trust")
	}
}
