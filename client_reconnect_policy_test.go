package sendspin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func TestRunRecoveryDependsOnActualSessionPurposeAndFailure(t *testing.T) {
	for _, mode := range []string{"initial-failure", "idle-drop", "playback-drop", "pairing-drop", "protocol-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			serverCred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			key := Secret{88}
			category := "lt"
			if mode == "pairing-drop" {
				key = cred.PairingPSK
				category = "pr"
			} else {
				cred.Records = []PairingRecord{{ServerID: serverCred.ClientID(), PSK: key}}
			}
			store := &MemoryStore{}
			if err = store.Save(ctx, cred); err != nil {
				t.Fatal(err)
			}
			client, err := New(ctx, Config{Name: "Recovery purpose", Store: store, Roles: []Role{}, HandshakeTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			first := make(chan error, 1)
			second := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					first <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if calls.Add(1) > 1 {
					second <- struct{}{}
					<-ctx.Done()
					return
				}
				h := sha256.Sum256(append([]byte("sendspin-psk-id-v1"), key[:]...))
				p := &handshakePeer{ctx: ctx, conn: conn, suite: wire.ChaCha, clientKey: mustPeerPublic(cred.PrivateKey[:]), serverKey: serverCred.PrivateKey[:], serverID: serverCred.ClientID(), pskID: base64.RawURLEncoding.EncodeToString(h[:]), psk: key[:]}
				run := func() error {
					init, err := p.readClientInit()
					if err != nil {
						return err
					}
					if mode == "initial-failure" {
						return nil
					}
					prologue, err := p.initialPrologue(init)
					if err != nil {
						return err
					}
					hs, raw, err := p.noise1(prologue, handshakeFault{body: fmt.Sprintf(`{"psk_id":"$PSK","psk_category":%q}`, category)})
					if err != nil {
						return err
					}
					if err = p.writeText(raw); err != nil {
						return err
					}
					if err = p.noise2(hs, false); err != nil {
						return err
					}
					if err = p.sendSecure(peerJSON("server/hello", map[string]any{"name": "Recovery peer"})); err != nil {
						return err
					}
					if kind, _, err := p.readSecure(); err != nil || kind != "client/hello" {
						return errors.New("hello missing")
					}
					activities := []string{}
					payload := map[string]any{"activities": activities, "active_roles": []Role{}}
					if mode == "playback-drop" {
						payload["activities"] = []string{"playback"}
					}
					if mode == "pairing-drop" {
						payload["activities"] = []string{"pairing"}
						payload["pairing"] = map[string]any{"method": "pairing_psk"}
					}
					if err = p.sendSecure(peerJSON("server/activate", payload)); err != nil {
						return err
					}
					if mode == "pairing-drop" {
						for _, expected := range []string{"client/pair-init", "client/pair-finalize"} {
							if kind, _, err := p.readSecure(); err != nil || kind != expected {
								return errors.New("pairing did not start")
							}
						}
					}
					if kind, _, err := p.readSecure(); err != nil || kind != "client/time" {
						return errors.New("activation did not complete")
					}
					if mode == "protocol-failure" {
						if err = p.sendSecure(peerJSON("client/command", map[string]any{})); err != nil {
							return err
						}
						return p.closedWithoutData(true)
					}
					return nil
				}
				first <- run()
			}))
			t.Cleanup(func() { cancel(); server.Close() })
			finished := make(chan error, 1)
			go func() { finished <- client.Run(ctx, "ws"+strings.TrimPrefix(server.URL, "http")) }()
			select {
			case err := <-first:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("first connection did not reach test boundary")
			}
			retry := mode == "initial-failure" || mode == "idle-drop" || mode == "playback-drop"
			select {
			case <-second:
				cancel()
				err = <-finished
				if !retry {
					t.Fatal("Run automatically resumed a terminated/protocol-failed pairing session")
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatal("retry cancellation lost", err)
				}
			case err = <-finished:
				if retry || err == nil || ctx.Err() != nil {
					t.Fatal("Run did not apply purpose-specific recovery", mode, err)
				}
				if mode == "protocol-failure" && !errors.Is(err, protocol.ErrProtocol) {
					t.Fatal("protocol failure was hidden", err)
				}
			case <-ctx.Done():
				t.Fatal("Run recovery did not terminate/retry")
			}
		})
	}
}

func TestReconnectReasonVocabularyAndBackoffPolicy(t *testing.T) {
	for _, reason := range []string{"another_server", "shutdown", "restart", "user_request", "unauthorized", "pairing_required", "concurrent_attempt", "unpaired", "unknown"} {
		err := fmt.Errorf("wrapped: %w", &GoodbyeError{Reason: reason})
		if got := shouldReconnect(err); got != (reason == "restart") {
			t.Fatal("wrong explicit reason policy", reason, got)
		}
	}
	transient := errors.New("transport dropped")
	for _, tc := range []struct {
		err   error
		retry bool
	}{{transient, true}, {context.DeadlineExceeded, true}, {protocol.ErrProtocol, false}, {fmt.Errorf("wrapped: %w", protocol.ErrProtocol), false}, {&pairingSessionEnded{cause: transient}, false}, {nil, false}} {
		if shouldReconnect(tc.err) != tc.retry {
			t.Fatal("wrong error recovery policy", tc.err)
		}
	}
	for _, tc := range []struct{ previous, connected, wait, next time.Duration }{{time.Second, 0, time.Second, 2 * time.Second}, {32 * time.Second, 0, 32 * time.Second, time.Minute}, {time.Minute, 0, time.Minute, time.Minute}, {time.Minute, 10 * time.Second, time.Minute, time.Minute}, {time.Minute, 10*time.Second + time.Nanosecond, time.Second, 2 * time.Second}} {
		wait, next := reconnectBackoff(tc.previous, tc.connected)
		if wait != tc.wait || next != tc.next {
			t.Fatal("backoff lost cap/reset", tc, wait, next)
		}
	}
	wrapped := &pairingSessionEnded{cause: transient}
	if !errors.Is(wrapped, transient) {
		t.Fatal("purpose annotation hid original error")
	}
}
