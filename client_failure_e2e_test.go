//go:build e2e

package sendspin

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

type outboundTestConnection struct {
	done   chan struct{}
	err    error // Read only after done closes.
	cancel context.CancelFunc
}

func connectReference(t *testing.T, client *Client, ref referenceServer) *outboundTestConnection {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	connection := &outboundTestConnection{done: make(chan struct{}), cancel: cancel}
	go func() {
		connection.err = client.Connect(ctx, "ws"+ref.url[len("http"):]+"/sendspin")
		close(connection.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-connection.done:
		case <-time.After(5 * time.Second):
			t.Error("outbound connection did not stop")
		}
	})
	return connection
}

func TestE2EReferenceClientCredentialLossAndRepair(t *testing.T) {
	for _, allowUnpaired := range []bool{false, true} {
		name := "paired-only"
		if allowUnpaired {
			name = "unpaired-access-enabled"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			oldPSK := Secret{55}
			ref := startReferenceWithOptions(t, cred, oldPSK, false, map[string]any{"device_pairing_psk": cred.PairingPSK})
			other, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			// Lose only this server's pairing, not the device identity or other records.
			cred.Records = []PairingRecord{{ServerID: other.ClientID(), PSK: Secret{77}}}
			store := FileStore{Path: filepath.Join(t.TempDir(), "device.json")}
			if err = store.Save(ctx, cred); err != nil {
				t.Fatal(err)
			}
			client, err := New(ctx, Config{Name: "Credential loss", Store: store, Roles: []Role{Controller}, AllowUnpaired: allowUnpaired})
			if err != nil {
				t.Fatal(err)
			}
			connection := connectReference(t, client, ref)
			eventually(t, func() bool {
				return client.Snapshot().ServerID == ref.id && referenceEventCount(t, ref, "credential_mismatch", "") > 0
			})
			ref.control(t, map[string]any{"command": "trust_unpaired"})
			status := ref.control(t, map[string]any{"command": "status"})
			if len(status["active_roles"].([]any)) != 0 || client.Snapshot().Available {
				t.Fatal("credential mismatch bypassed the existing record")
			}
			if err = client.Command(ctx, Command{Command: "play"}); !errors.Is(err, ErrInactiveRole) {
				t.Fatal("inactive role accepted a command", err)
			}
			got, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Records) != 1 || got.Records[0] != cred.Records[0] || got.PrivateKey != cred.PrivateKey || got.PairingPSK != cred.PairingPSK {
				t.Fatal("Sentinel fallback changed persisted credentials")
			}
			select {
			case <-connection.done:
				t.Fatal("fallback disconnected instead of keeping an idle session", connection.err)
			default:
			}
			ref.control(t, map[string]any{"command": "pair"})
			eventually(t, func() bool { return client.Snapshot().Available })
			got, err = store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Records) != 2 || got.Records[0] != cred.Records[0] || got.Records[1].ServerID != ref.id || got.Records[1].PSK == oldPSK {
				t.Fatal("re-pairing failed to install a fresh bound record")
			}
		})
	}
}

func TestE2EReferenceServerCredentialLossPreservesClientRecord(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{66}
	ref := startReferenceWithOptions(t, cred, psk, false, map[string]any{"device_pairing_psk": cred.PairingPSK})
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := FileStore{Path: filepath.Join(t.TempDir(), "device.json")}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	config := Config{Name: "Server credential loss", Store: store, Roles: []Role{Controller}, DisableDiscovery: true}
	client, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, stop := serveTestClient(t, client)
	ref.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool { return client.Snapshot().Available })
	stop()
	ref.control(t, map[string]any{"command": "forget"})
	restarted, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ = serveTestClient(t, restarted)
	ref.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool { return restarted.Snapshot().ServerID == ref.id })
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0] != cred.Records[0] || restarted.Snapshot().Available {
		t.Fatal("server-side loss silently removed or bypassed the client's record")
	}
	ref.control(t, map[string]any{"command": "pair"})
	eventually(t, func() bool { return restarted.Snapshot().Available })
	got, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0].ServerID != ref.id || got.Records[0].PSK == psk {
		t.Fatal("repair did not replace stale credentials")
	}
}

func TestE2EReferenceInjectedProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		message  any
		binary   []byte
		goodbye  string
		unpaired bool
	}{
		{name: "wrong-direction", message: map[string]any{"type": "client/state", "payload": map[string]any{"available": true}}},
		{name: "malformed-envelope", message: map[string]any{"type": "server/state", "payload": []any{}}},
		{name: "null-active-roles", message: map[string]any{"type": "server/activate", "payload": map[string]any{"activities": []string{}, "active_roles": nil}}},
		{name: "unknown-role-activation", message: map[string]any{"type": "server/activate", "payload": map[string]any{"activities": []string{}, "active_roles": []string{"player@v1"}}}},
		{name: "duplicate-roles", message: map[string]any{"type": "server/activate", "payload": map[string]any{"activities": []string{}, "active_roles": []string{"controller@v1", "controller@v1"}}}},
		{name: "paired-session-cannot-pair", message: map[string]any{"type": "server/activate", "payload": map[string]any{"activities": []string{"pairing"}, "active_roles": []string{}, "pairing": map[string]any{"method": "pairing_psk"}}}, goodbye: "unauthorized"},
		{name: "unpaired-playback-denied", unpaired: true, message: map[string]any{"type": "server/activate", "payload": map[string]any{"activities": []string{"playback"}, "active_roles": []string{"controller@v1"}}}, goodbye: "pairing_required"},
		{name: "incomplete-controller-state", message: map[string]any{"type": "server/state", "payload": map[string]any{"controller": map[string]any{"supported_commands": []string{}}}}},
		{name: "unexpected-pair-init", message: map[string]any{"type": "server/pair-init", "payload": map[string]any{}}},
		{name: "unexpected-pair-auth", message: map[string]any{"type": "server/pair-auth", "payload": map[string]any{}}},
		{name: "unexpected-pair-confirm", message: map[string]any{"type": "server/pair-confirm", "payload": map[string]any{}}},
		{name: "wrong-direction-pair-auth", message: map[string]any{"type": "client/pair-auth", "payload": map[string]any{}}},
		{name: "audio-without-stream", binary: []byte{4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			psk := Secret{88}
			ref := startReferenceWithOptions(t, cred, psk, false, map[string]any{"no_record": tc.unpaired})
			if !tc.unpaired {
				cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
			}
			store := &MemoryStore{}
			if err = store.Save(ctx, cred); err != nil {
				t.Fatal(err)
			}
			client, err := New(ctx, Config{Name: "Failure injection", Store: store, Roles: []Role{Controller}})
			if err != nil {
				t.Fatal(err)
			}
			connection := connectReference(t, client, ref)
			eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
			if tc.binary != nil {
				ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString(tc.binary)})
			} else {
				ref.control(t, map[string]any{"command": "send", "message": tc.message})
			}
			select {
			case <-connection.done:
			case <-time.After(3 * time.Second):
				t.Fatal("client accepted protocol violation")
			}
			if tc.goodbye != "" {
				var goodbye *GoodbyeError
				if !errors.As(connection.err, &goodbye) || goodbye.Reason != tc.goodbye {
					t.Fatal("incorrect refusal", connection.err)
				}
				eventually(t, func() bool { return referenceEventCount(t, ref, "disconnected", tc.goodbye) > 0 })
			} else {
				if !errors.Is(connection.err, protocol.ErrProtocol) {
					t.Fatal("expected protocol violation", connection.err)
				}
				eventually(t, func() bool { return referenceEventCount(t, ref, "disconnected", "") > 0 })
				for _, raw := range ref.control(t, map[string]any{"command": "status"})["events"].([]any) {
					event := raw.(map[string]any)
					if event["type"] == "disconnected" && event["reason"] != nil {
						t.Fatal("silent failure sent an application goodbye", event)
					}
				}
			}
		})
	}
}

func TestE2EReferenceUnknownExtensionsAreIgnored(t *testing.T) {
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{99}
	ref := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Extension handling", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	for _, kind := range []string{"future/informational", "", "future", "  "} {
		ref.control(t, map[string]any{"command": "send", "message": map[string]any{"type": kind, "payload": map[string]any{"anything": nil}}})
	}
	ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString([]byte{240, 1, 2, 3})})
	// A same-direction marker proves the actor has processed all preceding
	// extensions; an opposite-direction availability update alone cannot.
	ref.control(t, map[string]any{"command": "send", "message": map[string]any{"type": "group/update", "payload": map[string]any{"playback_state": "stopped", "group_id": "extension-barrier", "group_name": "extensions processed"}}})
	eventually(t, func() bool { return client.Snapshot().Group.ID == "extension-barrier" })
	if err = client.SetAvailable(ctx, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return ref.control(t, map[string]any{"command": "status"})["available"] == false })
	if err = client.SetAvailable(ctx, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return ref.control(t, map[string]any{"command": "status"})["available"] == true })
	select {
	case <-connection.done:
		t.Fatal("unknown extension closed connection", connection.err)
	default:
	}
}

func TestE2EReferenceRejectsInvalidPlayerCodecHeaders(t *testing.T) {
	for _, test := range []struct {
		name, codec string
		header      *string
		custom      bool
	}{
		{"pcm-empty", "pcm", stringPointer(""), false}, {"pcm-header", "pcm", stringPointer("ZkxhQw=="), false},
		{"opus-empty", "opus", stringPointer(""), false}, {"opus-header", "opus", stringPointer("T3B1c0hlYWQ="), false},
		{"flac-missing", "flac", nil, false}, {"flac-empty", "flac", stringPointer(""), false},
		{"flac-custom-crlf", "flac", stringPointer("Zkxh\r\nQw=="), true},
		{"flac-custom-unpadded", "flac", stringPointer("ZkxhQw"), true},
		{"flac-custom-unused-bits", "flac", stringPointer("Zh=="), true},
		{"flac-custom-url-alphabet", "flac", stringPointer("-_8="), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			cred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			psk := Secret{103}
			ref := startReference(t, cred, psk, false)
			cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
			store := &MemoryStore{}
			if err = store.Save(ctx, cred); err != nil {
				t.Fatal(err)
			}
			formats := []codec.Format{{Codec: test.codec, SampleRate: 48000, Channels: 1, BitDepth: 16}}
			if test.codec == "opus" {
				formats = append(formats, codec.Format{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16})
			}
			called := false
			var factories map[string]codec.Factory
			if test.custom {
				factories = map[string]codec.Factory{"flac": func(codec.Format, string) (codec.AudioDecoder, error) { called = true; return unusedDecoder{}, nil }}
			}
			client, err := New(ctx, Config{Name: "Header failure", Store: store, Roles: []Role{Player}, Output: clockOutput{}, Formats: formats, Decoders: factories})
			if err != nil {
				t.Fatal(err)
			}
			connection := connectReference(t, client, ref)
			eventually(t, func() bool { return client.Snapshot().Available })
			player := map[string]any{"codec": test.codec, "sample_rate": 48000, "channels": 1, "bit_depth": 16}
			if test.header != nil {
				player["codec_header"] = *test.header
			}
			ref.control(t, map[string]any{"command": "send", "message": map[string]any{"type": "stream/start", "payload": map[string]any{"server_transmitted": 0, "player": player}}})
			select {
			case <-connection.done:
			case <-time.After(3 * time.Second):
				t.Fatal("invalid codec header was accepted")
			}
			if !errors.Is(connection.err, protocol.ErrProtocol) {
				t.Fatal("expected protocol violation", connection.err)
			}
			if called {
				t.Fatal("invalid Base64 reached the custom factory")
			}
			eventually(t, func() bool { return referenceEventCount(t, ref, "disconnected", "") > 0 })
			for _, raw := range ref.control(t, map[string]any{"command": "status"})["events"].([]any) {
				event := raw.(map[string]any)
				if event["type"] == "disconnected" && event["reason"] != nil {
					t.Fatal("protocol failure sent goodbye", event)
				}
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
