//go:build e2e

package sendspin

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func helloFieldClient(t *testing.T, payload map[string]any, observe diagnostic.Observer) (*Client, referenceServer, *MemoryStore, *outboundTestConnection) {
	t.Helper()
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{120}
	ref := startReferenceWithOptions(t, cred, psk, false, map[string]any{"observe_pairing": true, "initial_hello": payload, "device_pairing_psk": cred.PairingPSK})
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Hello fields", Store: store, Roles: []Role{Controller}, Observe: observe})
	if err != nil {
		t.Fatal(err)
	}
	return client, ref, store, connectReference(t, client, ref)
}

func TestReferenceRejectsMalformedServerHelloBeforeReply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
	}{
		{"null-languages", map[string]any{"name": "server", "languages": nil}},
		{"empty-languages", map[string]any{"name": "server", "languages": []string{}}},
		{"scalar-languages", map[string]any{"name": "server", "languages": "de"}},
		{"null-language", map[string]any{"name": "server", "languages": []any{nil}}},
		{"numeric-language", map[string]any{"name": "server", "languages": []any{4}}},
		{"underscore-tag", map[string]any{"name": "server", "languages": []string{"de_DE"}}},
		{"repeated-variant", map[string]any{"name": "server", "languages": []string{"de-1901-1901"}}},
		{"repeated-extension", map[string]any{"name": "server", "languages": []string{"en-a-foo-A-bar"}}},
		{"unknown-irregular-tag", map[string]any{"name": "server", "languages": []string{"i-future"}}},
		{"non-ascii-tag", map[string]any{"name": "server", "languages": []string{"en-\u212a"}}},
		{"null-source-support", map[string]any{"name": "server", "source@v1_support": nil}},
		{"array-source-support", map[string]any{"name": "server", "source@v1_support": []any{}}},
		{"missing-name", map[string]any{"languages": []string{"de"}}},
		{"numeric-name", map[string]any{"name": 42}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var applicationWrites atomic.Int32
			client, _, store, connection := helloFieldClient(t, tc.payload, func(event diagnostic.Message) {
				if event.Direction == diagnostic.Sent && event.Encrypted {
					applicationWrites.Add(1)
				}
			})
			select {
			case <-connection.done:
			case <-time.After(3 * time.Second):
				t.Fatal("malformed hello left connection open")
			}
			if !errors.Is(connection.err, protocol.ErrProtocol) {
				t.Fatal("expected protocol error", connection.err)
			}
			if applicationWrites.Load() != 0 {
				t.Fatal("invalid hello caused encrypted application writes")
			}
			state := client.Snapshot()
			if state.ServerID != "" || state.ServerLanguages != nil {
				t.Fatal("invalid hello became admitted state")
			}
			cred, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(cred.Records) != 1 || cred.Records[0].PSK != (Secret{120}) {
				t.Fatal("hello failure removed or replaced credentials")
			}
		})
	}
}

func TestReferenceServerLanguageHintsRemainVerbatimAcrossRehandshake(t *testing.T) {
	want := []string{"DE-de", "i-klingon", "x-vendor", "zz-Zzzz-999-future1", "DE-de"}
	var hellos atomic.Int32
	client, ref, _, connection := helloFieldClient(t, map[string]any{
		"name": "Authoritative wire name", "languages": want,
		"NAME": nil, "LANGUAGES": false, "future_info": nil, "future@v2_support": []any{},
		// Source is not implemented, so only the declared outer object shape
		// is consumed. This does not claim a conformant source capability.
		"source@v1_support": map[string]any{"future": nil, "supported_formats": false},
	}, func(event diagnostic.Message) {
		if event.Direction == diagnostic.Sent && event.Type == "client/hello" {
			hellos.Add(1)
		}
	})
	eventually(t, func() bool { return client.Snapshot().Available })
	state := client.Snapshot()
	if state.ServerName != "Authoritative wire name" || !slices.Equal(state.ServerLanguages, want) {
		t.Fatal("server hello hint was rewritten or case-lookalike took effect", state.ServerName, state.ServerLanguages)
	}
	state.ServerLanguages[0] = "consumer mutation"
	if err := client.SetAvailable(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := client.SetAvailable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ref.control(t, map[string]any{"command": "pair"})
	eventually(t, func() bool { return client.Snapshot().Available })
	if !slices.Equal(client.Snapshot().ServerLanguages, want) || hellos.Load() != 1 {
		t.Fatal("re-handshake reset hints or repeated client/hello")
	}
	// A subsequent server/hello is not an update mechanism, even if valid.
	sendPairingFixtureMessage(t, ref, "server/hello", map[string]any{"name": "replacement", "languages": []string{"en"}})
	requireSilentProtocolClose(t, connection, ref)
	if hellos.Load() != 1 {
		t.Fatal("duplicate server hello caused another client hello")
	}
}

func TestReferenceServerHelloLanguagesAreOptional(t *testing.T) {
	client, _, _, _ := helloFieldClient(t, map[string]any{"name": "", "future@v2_support": nil}, nil)
	eventually(t, func() bool { return client.Snapshot().Available })
	if state := client.Snapshot(); state.ServerName != "" || state.ServerLanguages != nil {
		t.Fatal("absent hint was invented or empty friendly name rejected")
	}
}
