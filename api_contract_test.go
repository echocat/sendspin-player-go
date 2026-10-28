package sendspin

import (
	"context"
	"errors"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

type aliasingStore struct{ value Credentials }

func (s *aliasingStore) Load(ctx context.Context) (Credentials, error) { return s.value, ctx.Err() }
func (s *aliasingStore) Save(ctx context.Context, c Credentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.value = c
	return nil
}

func TestCredentialStoreBoundariesDoNotAliasClientRecords(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	cred.Records = []PairingRecord{{ServerID: peer.ClientID(), PSK: Secret{57}}}
	store := &aliasingStore{value: cred}
	c, err := New(ctx, Config{Name: "Ownership review", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	store.value.Records[0].PSK[0] = 58
	if c.credentials.Records[0].PSK[0] != 57 {
		t.Fatal("Load retained Store-owned slice")
	}
	if err = c.update(ctx, func(next *Credentials) error { next.Volume = 17; return nil }); err != nil {
		t.Fatal(err)
	}
	store.value.Records[0].PSK[0] = 59
	if c.credentials.Records[0].PSK[0] != 57 || c.credentials.Volume != 17 {
		t.Fatal("Save exposed SDK-owned credential slice")
	}
}

func TestPublicProtocolSentinelPreservesErrorIdentity(t *testing.T) {
	if ErrProtocol != protocol.ErrProtocol || !errors.Is(ErrProtocol, protocol.ErrProtocol) {
		t.Fatal("public alias changed protocol error identity")
	}
}
