package wire

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/flynn/noise"
)

func TestEncryptedChannelAndFailureHandling(t *testing.T) {
	for _, fault := range []string{"none", "fragmented", "replay", "tamper", "cleartext", "orphan-fragment"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			clientKey, err := noise.DH25519.GenerateKeypair(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			serverKey, err := noise.DH25519.GenerateKeypair(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			psk := bytes.Repeat([]byte{7}, 32)
			idHash := sha256.Sum256(append([]byte("sendspin-psk-id-v1"), psk...))
			id := base64.RawURLEncoding.EncodeToString(idHash[:])
			serverDone := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, e := websocket.Accept(w, r, nil)
				if e != nil {
					serverDone <- e
					return
				}
				defer func() { _ = conn.CloseNow() }()
				conn.SetReadLimit(65535)
				serve := func() error {
					_, init, e := conn.Read(ctx)
					if e != nil {
						return e
					}
					// Deliberately non-canonical serialization. Re-encoding this init
					// instead of hashing the exact raw bytes breaks the handshake.
					raw := []byte(fmt.Sprintf(" {\n\"payload\": {\"version\":1, \"server_id\":%q}, \"type\":\"server/init\" } ", base64.RawURLEncoding.EncodeToString(serverKey.Public)))
					if e = conn.Write(ctx, websocket.MessageText, raw); e != nil {
						return e
					}
					hs, e := noise.NewHandshakeState(noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256), Pattern: noise.HandshakeKK, Initiator: true, PresharedKey: psk, PresharedKeyPlacement: 2, StaticKeypair: serverKey, PeerStatic: clientKey.Public, Prologue: append(append([]byte(nil), init...), raw...)})
					if e != nil {
						return e
					}
					inner, _ := json.Marshal(map[string]string{"psk_id": id, "psk_category": "lt"})
					msg, _, _, e := hs.WriteMessage(nil, inner)
					if e != nil {
						return e
					}
					payload, _ := protocol.Encode("noise/handshake", map[string]string{"data": base64.RawURLEncoding.EncodeToString(msg)})
					if e = conn.Write(ctx, websocket.MessageText, payload); e != nil {
						return e
					}
					_, answer, e := conn.Read(ctx)
					if e != nil {
						return e
					}
					m, e := protocol.Parse(answer)
					if e != nil {
						return e
					}
					var body struct {
						Data string `json:"data"`
					}
					if e = json.Unmarshal(m.Payload, &body); e != nil {
						return e
					}
					answer, e = base64.RawURLEncoding.DecodeString(body.Data)
					if e != nil {
						return e
					}
					plain, outgoing, incoming, e := hs.ReadMessage(nil, answer)
					if e != nil {
						return e
					}
					if string(plain) != "{}" {
						return errors.New("handshake payload was not exactly {}")
					}
					if fault == "fragmented" {
						return protocol.Fragment(append([]byte{4}, bytes.Repeat([]byte{0xaa}, 150000)...), func(frame []byte) error {
							packet, err := outgoing.Encrypt(nil, nil, frame)
							if err != nil {
								return err
							}
							return conn.Write(ctx, websocket.MessageBinary, packet)
						})
					}
					packet, e := outgoing.Encrypt(nil, nil, []byte{0, '{', '}'})
					if e != nil {
						return e
					}
					if e = conn.Write(ctx, websocket.MessageBinary, packet); e != nil {
						return e
					}
					if fault == "cleartext" {
						return conn.Write(ctx, websocket.MessageText, []byte(`{"type":"x","payload":{}}`))
					}
					if fault == "orphan-fragment" {
						malformed, err := outgoing.Encrypt(nil, nil, []byte{1, 0})
						if err != nil {
							return err
						}
						return conn.Write(ctx, websocket.MessageBinary, malformed)
					}
					if fault == "tamper" {
						packet = append([]byte(nil), packet...)
						packet[0] ^= 1
					}
					if fault != "none" {
						return conn.Write(ctx, websocket.MessageBinary, packet)
					}
					rsm := protocol.Reassembler{Limit: 300000}
					for {
						_, ciphertext, e := conn.Read(ctx)
						if e != nil {
							return e
						}
						p, e := incoming.Decrypt(nil, nil, ciphertext)
						if e != nil {
							return e
						}
						result, e := rsm.Push(p, func(byte) bool { return true })
						if e != nil {
							return e
						}
						if result != nil {
							if len(result) != 150001 || result[0] != 0 {
								return errors.New("fragmented client message differs")
							}
							return nil
						}
					}
				}
				serverDone <- serve()
			}))
			defer server.Close()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.CloseNow() }()
			var observed []diagnostic.Message
			ch, err := Handshake(ctx, conn, clientKey, ChaCha, 300000, func(string) Resolve {
				return func(got, category string, initial bool) (Selection, error) {
					if got != id || category != "lt" || !initial {
						return Selection{}, errors.New("bad selection")
					}
					return Selection{PSK: psk, Category: category}, nil
				}
			}, func(m diagnostic.Message) { observed = append(observed, m) })
			if err != nil {
				t.Fatal(err)
			}
			first, at, err := ch.ReadTimed(ctx)
			if fault == "fragmented" {
				if err != nil || len(first) != 150001 || first[0] != 4 || len(observed) != 8 {
					t.Fatalf("fragmented receive: %d bytes, %d events, %v", len(first), len(observed), err)
				}
				for _, event := range observed[4:7] {
					if event.Reassembled || event.MessageID != 1 || len(event.Prefix) != 1 {
						t.Fatal("fragment body leaked")
					}
				}
				logical := observed[7]
				if !logical.Reassembled || logical.MessageID != 4 || len(logical.Prefix) != 13 || logical.At != at {
					t.Fatal("missing logical/arrival observation")
				}
				if err = <-serverDone; err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != nil || !bytes.Equal(first, []byte{0, '{', '}'}) {
				t.Fatal(first, err)
			}
			if len(observed) != 5 || at != observed[4].At {
				t.Fatalf("missing transport/arrival observations: %+v", observed)
			}
			for _, event := range observed[:4] {
				if event.Encrypted || event.Transport != "text" {
					t.Fatal("bad handshake evidence")
				}
				if event.Type == "noise/handshake" && len(event.Payload) != 0 {
					t.Fatal("Noise data leaked")
				}
			}
			if !observed[4].Encrypted || observed[4].Transport != "binary" {
				t.Fatal("missing authenticated frame")
			}
			before := len(observed)
			if fault == "none" {
				frame := append([]byte{0}, bytes.Repeat([]byte{42}, 150000)...)
				if err = ch.Send(ctx, frame); err != nil {
					t.Fatal(err)
				}
				if len(observed) != before+4 || !observed[len(observed)-1].Reassembled {
					t.Fatal("missing fragmented/logical send observations")
				}
			} else {
				if _, err = ch.Read(ctx); err == nil {
					t.Fatal("accepted", fault)
				}
				if fault == "orphan-fragment" && !errors.Is(err, protocol.ErrProtocol) {
					t.Fatal("expected fragment sequence violation", err)
				}
				if fault != "orphan-fragment" && len(observed) != before {
					t.Fatal("unauthenticated frame was observed")
				}
			}
			if err = <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
