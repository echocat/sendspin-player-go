// Package wire implements the encrypted RC1 WebSocket channel.
package wire

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/flynn/noise"
)

const (
	ChaCha = "25519_ChaChaPoly_SHA256"
	AES    = "25519_AESGCM_SHA256"
)

type Selection struct {
	PSK      []byte
	Category string
}

// Resolve must verify the stored server binding, not just the PSK hash.
type Resolve func(id, category string, initial bool) (Selection, error)

// Channel is single-writer/single-reader. Handshake and Rehandshake must run
// with application writes paused. Cipher states never escape this package.
type Channel struct {
	Conn          *websocket.Conn
	ServerID      string
	Category      string
	suite         string
	key           noise.DHKey
	peer          []byte
	resolve       Resolve
	send, receive *noise.CipherState
	hash          []byte
	fragments     protocol.Reassembler
	observer      diagnostic.Observer
}

func Handshake(ctx context.Context, conn *websocket.Conn, key noise.DHKey, suite string, limit int, resolve func(string) Resolve, observer diagnostic.Observer) (*Channel, error) {
	c := &Channel{Conn: conn, key: key, suite: suite, fragments: protocol.Reassembler{Limit: limit}, observer: observer}
	// The cleartext carrier contains base64-encoded Noise bytes plus JSON.
	// A valid 65535-byte Noise handshake alone encodes to 87380 characters.
	// Keep pre-authentication input bounded, without applying the smaller raw
	// transport-packet cap to its textual carrier.
	conn.SetReadLimit(128 << 10)
	init, err := protocol.Encode("client/init", struct {
		ClientID string `json:"client_id"`
		Version  int    `json:"version"`
		Suite    string `json:"suite"`
	}{base64.RawURLEncoding.EncodeToString(key.Public), protocol.CoreVersion, suite})
	if err != nil {
		return nil, err
	}
	if err = conn.Write(ctx, websocket.MessageText, init); err != nil {
		return nil, err
	}
	c.observeJSON(init, diagnostic.Sent, "text", false, false, monotime.Now())
	typ, raw, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, protocol.ErrProtocol
	}
	c.observeJSON(raw, diagnostic.Received, "text", false, false, monotime.Now())
	m, err := protocol.Parse(raw)
	if err != nil {
		return nil, err
	}
	if m.Type == "server/error" {
		return nil, fmt.Errorf("sendspin: server rejected init")
	}
	if m.Type != "server/init" {
		return nil, protocol.ErrProtocol
	}
	var si struct {
		ServerID string `json:"server_id"`
		Version  int    `json:"version"`
	}
	if err = protocol.Decode(m.Payload, &si, "server_id", "version"); err != nil {
		return nil, err
	}
	peer, err := decodeBase64URL(si.ServerID)
	if err != nil || len(peer) != 32 || si.Version != protocol.CoreVersion {
		return nil, protocol.ErrProtocol
	}
	c.ServerID, c.peer, c.resolve = si.ServerID, peer, resolve(si.ServerID)
	prologue := append(append([]byte(nil), init...), raw...)
	typ, raw, err = conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, protocol.ErrProtocol
	}
	c.observeJSON(raw, diagnostic.Received, "text", false, false, monotime.Now())
	m, err = protocol.Parse(raw)
	if err != nil {
		return nil, err
	}
	if m.Type != "noise/handshake" {
		return nil, protocol.ErrProtocol
	}
	if err = c.exchange(ctx, m.Payload, prologue, true); err != nil {
		return nil, err
	}
	conn.SetReadLimit(65535)
	return c, nil
}

func (c *Channel) exchange(ctx context.Context, payload, prologue []byte, initial bool) error {
	var h struct {
		Data string `json:"data"`
	}
	if err := protocol.Decode(payload, &h, "data"); err != nil {
		return err
	}
	data, err := decodeBase64URL(h.Data)
	if err != nil || len(data) > 65535 {
		return protocol.ErrProtocol
	}
	cipher := noise.CipherChaChaPoly
	if c.suite == AES {
		cipher = noise.CipherAESGCM
	} else if c.suite != ChaCha {
		return protocol.ErrProtocol
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		Random:      rand.Reader,
		Initiator:   false,
		CipherSuite: noise.NewCipherSuite(noise.DH25519, cipher, noise.HashSHA256),
		Pattern:     noise.HandshakeKK, PresharedKeyPlacement: 2,
		StaticKeypair: c.key, PeerStatic: c.peer, Prologue: prologue,
	})
	if err != nil {
		return err
	}
	inner, _, _, err := hs.ReadMessage(nil, data)
	if err != nil {
		return err
	}
	var p struct {
		ID       string `json:"psk_id"`
		Category string `json:"psk_category"`
	}
	if err = protocol.Decode(inner, &p, "psk_id", "psk_category"); err != nil {
		return err
	}
	id, err := decodeBase64URL(p.ID)
	if err != nil || len(id) != 32 || (p.Category != "lt" && p.Category != "pr" && p.Category != "sn") {
		return protocol.ErrProtocol
	}
	selection, err := c.resolve(p.ID, p.Category, initial)
	if err != nil {
		return err
	}
	if err = hs.SetPresharedKey(selection.PSK); err != nil {
		return err
	}
	answer, incoming, outgoing, err := hs.WriteMessage(nil, []byte("{}"))
	if err != nil {
		return err
	}
	encoded, err := protocol.Encode("noise/handshake", map[string]string{"data": base64.RawURLEncoding.EncodeToString(answer)})
	if err != nil {
		return err
	}
	if initial {
		err = c.Conn.Write(ctx, websocket.MessageText, encoded)
		if err == nil {
			c.observeJSON(encoded, diagnostic.Sent, "text", false, false, monotime.Now())
		}
	} else {
		err = c.Send(ctx, append([]byte{0}, encoded...))
	}
	if err != nil {
		return err
	}
	c.receive, c.send, c.Category = incoming, outgoing, selection.Category
	c.hash = append([]byte(nil), hs.ChannelBinding()...)
	return nil
}

// Rehandshake preserves reassembly state and upper-layer stream/time state.
// The caller must require server/activate as the next application message.
func (c *Channel) Rehandshake(ctx context.Context, payload json.RawMessage) error {
	return c.exchange(ctx, payload, c.hash, false)
}

func (c *Channel) Send(ctx context.Context, frame []byte) error {
	err := protocol.Fragment(frame, func(p []byte) error {
		ciphertext, err := c.send.Encrypt(nil, nil, p)
		if err != nil {
			return err
		}
		if err = c.Conn.Write(ctx, websocket.MessageBinary, ciphertext); err != nil {
			return err
		}
		c.observeFrame(p, diagnostic.Sent, false, monotime.Now())
		return nil
	})
	if err == nil && len(frame) > protocol.MaxPlaintext {
		c.observeFrame(frame, diagnostic.Sent, true, monotime.Now())
	}
	return err
}

func (c *Channel) JSON(ctx context.Context, kind string, payload any) error {
	data, err := protocol.Encode(kind, payload)
	if err != nil {
		return err
	}
	return c.Send(ctx, append([]byte{0}, data...))
}

func (c *Channel) Read(ctx context.Context) ([]byte, error) {
	frame, _, err := c.ReadTimed(ctx)
	return frame, err
}

// ReadTimed captures the application-boundary arrival before invoking observers.
func (c *Channel) ReadTimed(ctx context.Context) ([]byte, time.Time, error) {
	for {
		typ, ciphertext, err := c.Conn.Read(ctx)
		if err != nil {
			return nil, time.Time{}, err
		}
		if typ != websocket.MessageBinary || len(ciphertext) > 65535 {
			return nil, time.Time{}, protocol.ErrProtocol
		}
		plain, err := c.receive.Decrypt(nil, nil, ciphertext)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("sendspin: channel authentication failed")
		}
		frame, err := c.fragments.Push(plain, func(id byte) bool { return id == 0 || id == 4 || (id >= 8 && id <= 11) || (id >= 16 && id <= 20) })
		arrival := monotime.Now()
		c.observeFrame(plain, diagnostic.Received, false, arrival)
		if len(plain) > 0 && plain[0] == 1 && frame != nil {
			c.observeFrame(frame, diagnostic.Received, true, arrival)
		}
		if err != nil || frame != nil {
			return frame, arrival, err
		}
	}
}
