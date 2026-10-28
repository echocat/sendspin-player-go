package sendspin

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func TestTransportReplayAndOutOfOrderCiphertextCloseActualHandlers(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"inbound", "outbound"} {
			for _, fault := range []string{"replay", "out-of-order"} {
				t.Run(suite+"/"+direction+"/"+fault, func(t *testing.T) {
					runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
						if err := p.establish(); err != nil {
							return err
						}
						makePacket := func(name string) ([]byte, error) {
							return p.out.Encrypt(nil, nil, append([]byte{0}, peerJSON("group/update", map[string]any{"group_id": name, "group_name": name, "playback_state": "stopped"})...))
						}
						first, err := makePacket("first")
						if err != nil {
							return err
						}
						if fault == "replay" {
							if err = p.conn.Write(p.ctx, websocket.MessageBinary, first); err != nil {
								return err
							}
							if err = p.conn.Write(p.ctx, websocket.MessageBinary, first); err != nil {
								return err
							}
						} else {
							second, err := makePacket("second")
							if err != nil {
								return err
							}
							if err = p.conn.Write(p.ctx, websocket.MessageBinary, second); err != nil {
								return err
							}
						}
						return p.closedWithoutData(true)
					})
				})
			}
		}
	}
}

func TestDiscardedUnknownAndUnavailableRoleFragmentsStillAuthenticateAndValidate(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"inbound", "outbound"} {
			for _, known := range []bool{false, true} {
				for _, fault := range []string{"tamper", "sequence"} {
					t.Run(fmt.Sprintf("%s/%s/known-%t/%s", suite, direction, known, fault), func(t *testing.T) {
						cfg := Config{Roles: []Role{Player, Controller}, Output: unusedOutput{}, Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}}}
						runConfiguredHandshakePeer(t, suite, direction, 2*time.Second, cfg, func(p *handshakePeer) error {
							if err := p.establish(); err != nil {
								return err
							}
							id := byte(240)
							if known {
								id = 4
								if err := p.sendSecure(peerJSON("server/activate", map[string]any{"activities": []string{}, "active_roles": []string{"player@v1"}})); err != nil {
									return err
								}
								kind, raw, err := p.readSecure()
								if err != nil {
									return err
								}
								if kind != "client/state" || !bytesContainUnavailable(raw) {
									return errors.New("player was not explicitly unavailable")
								}
								if err = p.sendSecure(peerJSON("stream/start", map[string]any{"server_transmitted": 0, "player": map[string]any{"codec": "pcm", "sample_rate": 48000, "channels": 1, "bit_depth": 16}})); err != nil {
									return err
								}
							}
							packet, err := p.out.Encrypt(nil, nil, []byte{1, 2, id, 11})
							if err != nil {
								return err
							}
							if err = p.conn.Write(p.ctx, websocket.MessageBinary, packet); err != nil {
								return err
							}
							plain := []byte{1, 1, 12}
							if fault == "sequence" {
								plain = []byte{1, 2, id, 12}
							}
							packet, err = p.out.Encrypt(nil, nil, plain)
							if err != nil {
								return err
							}
							if fault == "tamper" {
								packet[len(packet)-1] ^= 1
							}
							if err = p.conn.Write(p.ctx, websocket.MessageBinary, packet); err != nil {
								return err
							}
							return p.closedWithoutData(true)
						})
					})
				}
			}
		}
	}
}

func bytesContainUnavailable(raw []byte) bool {
	var p struct {
		Available *bool `json:"available"`
	}
	return json.Unmarshal(raw, &p) == nil && p.Available != nil && !*p.Available
}

func TestWebSocketControlFramesWorkAcrossInitialAndRekeyPhases(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"inbound", "outbound"} {
			t.Run(suite+"/"+direction, func(t *testing.T) {
				runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
					type message struct {
						typ websocket.MessageType
						raw []byte
						err error
					}
					incoming := make(chan message, 16)
					done := make(chan struct{})
					go func() {
						defer close(done)
						for {
							typ, raw, err := p.conn.Read(p.ctx)
							select {
							case incoming <- message{typ, raw, err}:
							case <-p.ctx.Done():
								return
							}
							if err != nil {
								return
							}
						}
					}()
					defer func() { _ = p.conn.CloseNow(); <-done }()
					p.readMessage = func() (websocket.MessageType, []byte, error) {
						select {
						case m := <-incoming:
							return m.typ, m.raw, m.err
						case <-p.ctx.Done():
							return 0, nil, p.ctx.Err()
						}
					}
					init, err := p.readClientInit()
					if err != nil {
						return err
					}
					if err = p.conn.Ping(p.ctx); err != nil {
						return err
					}
					prologue, err := p.initialPrologue(init)
					if err != nil {
						return err
					}
					if err = p.conn.Ping(p.ctx); err != nil {
						return err
					}
					hs, raw, err := p.noise1(prologue, handshakeFault{})
					if err != nil {
						return err
					}
					if err = p.writeText(raw); err != nil {
						return err
					}
					if err = p.noise2(hs, false); err != nil {
						return err
					}
					if err = p.conn.Ping(p.ctx); err != nil {
						return err
					}
					if err = p.activate(true); err != nil {
						return err
					}
					if err = p.conn.Ping(p.ctx); err != nil {
						return err
					}
					hs, raw, err = p.noise1(p.hash, handshakeFault{})
					if err != nil {
						return err
					}
					if err = p.sendSecure(raw); err != nil {
						return err
					}
					if err = p.noise2(hs, true); err != nil {
						return err
					}
					if err = p.conn.Ping(p.ctx); err != nil {
						return err
					}
					if err = p.activate(false); err != nil {
						return err
					}
					return p.conn.Close(websocket.StatusNormalClosure, "control-frame test")
				})
			})
		}
	}
}
