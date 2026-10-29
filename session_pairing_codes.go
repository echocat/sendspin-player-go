package sendspin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/echocat/sendspin-player-go/internal/cpace"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
	"golang.org/x/crypto/chacha20poly1305"
)

type codeAttempt struct {
	method, state     string
	round             uint32
	h, nonceA, nonceB []byte
	pin               PairingPIN
	pake              *cpace.Result
	displayed         bool
}

func (s *session) endCodePairing() {
	p := s.codePairing
	if p == nil {
		return
	}
	s.codePairing = nil
	if p.pake != nil {
		p.pake.Close()
	}
	clear(p.nonceB)
	p.pin = PairingPIN{}
	if p.displayed && s.client.cfg.CodePairing != nil && s.client.cfg.CodePairing.Display != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.client.cfg.CodePairing.Display(ctx, PairingDisplay{ServerID: s.channel.ServerID})
	}
}
func (s *session) startCodePairing(method string) error {
	h := s.channel.BindingHash()
	if len(h) != 32 {
		return protocol.ErrProtocol
	}
	s.codePairing = &codeAttempt{method: method, state: "pending", h: h}
	if s.codeReady() {
		return s.beginCodeAttempt()
	}
	s.client.emit(Event{Kind: "pairing_pending", ServerID: s.channel.ServerID})
	return s.send("client/pair-pending", map[string]uint32{"pairing_index": s.media.pairingIndex})
}
func (s *session) codeReady() bool {
	p := s.codePairing
	if p == nil || p.state != "pending" {
		return false
	}
	c := s.client
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.method == "dynamic_pairing_code" {
		return c.credentials.DynamicPairingRounds < 20
	}
	w := c.pairWindow
	return monotime.Now().Before(w.expires) && (w.owner == nil || w.owner == s) && w.failures < 5
}
func (s *session) advanceCodePairing() error {
	if s.codeReady() {
		return s.beginCodeAttempt()
	}
	return nil
}
func (s *session) beginCodeAttempt() error {
	p := s.codePairing
	if p.method == "static_pairing_code" {
		c := s.client
		c.mu.Lock()
		w := &c.pairWindow
		if !monotime.Now().Before(w.expires) || (w.owner != nil && w.owner != s) || w.failures >= 5 {
			c.mu.Unlock()
			return nil
		}
		w.owner = s
		p.pin = PairingPIN{string(c.credentials.StaticPairingPIN[:8])}
		c.mu.Unlock()
		p.round = 1
		p.state = "auth"
	} else {
		p.nonceB = make([]byte, 32)
		if _, err := rand.Read(p.nonceB); err != nil {
			return err
		}
		p.state = "init"
	}
	var psk Secret
	if _, err := rand.Read(psk[:]); err != nil {
		return err
	}
	s.pendingPSK = &psk
	s.pairingDeadline = monotime.Now().Add(2 * time.Minute)
	if !s.beginPairingAttempt() {
		s.endPairing(true)
		if err := s.send("pair/abort", map[string]string{"reason": "concurrent_attempt"}); err != nil {
			return err
		}
		return &GoodbyeError{"concurrent_attempt"}
	}
	fields := map[string]any{"pairing_index": s.media.pairingIndex}
	if p.method == "dynamic_pairing_code" {
		commit := sha256.Sum256(append([]byte("sendspin-pair-commit-v1"), p.nonceB...))
		fields["commit_B"] = base64.RawURLEncoding.EncodeToString(commit[:])
	}
	return s.send("client/pair-init", fields)
}
func codeBytes(encoded string, n int) ([]byte, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(b) != n || base64.RawURLEncoding.EncodeToString(b) != encoded {
		return nil, protocol.ErrProtocol
	}
	return b, nil
}
func (p *codeAttempt) sid(index uint32) []byte {
	b := append([]byte("sendspin-pair-pake-v1"), p.h...)
	b = binary.BigEndian.AppendUint32(b, index)
	return binary.BigEndian.AppendUint32(b, p.round)
}
func (s *session) codeMessage(kind string, data []byte) error {
	p := s.codePairing
	if p == nil || p.state == "pending" {
		return protocol.ErrProtocol
	}
	switch kind {
	case "server/pair-init":
		if p.method != "dynamic_pairing_code" || (p.state != "init" && p.state != "retry") {
			return protocol.ErrProtocol
		}
		var msg struct {
			Nonce *string `json:"nonce_A"`
		}
		if err := protocol.Decode(data, &msg); err != nil {
			return err
		}
		if p.round == 0 {
			if msg.Nonce == nil {
				return protocol.ErrProtocol
			}
			nonce, err := codeBytes(*msg.Nonce, 32)
			if err != nil {
				return err
			}
			p.nonceA = nonce
			b := append(append(append([]byte("sendspin-pairing-code-derive-v1"), p.h...), p.nonceA...), p.nonceB...)
			digest := sha256.Sum256(b)
			var v uint32
			for _, b := range digest {
				v = (v*256 + uint32(b)) % 1000000
			}
			p.pin = PairingPIN{fmt.Sprintf("%06d", v)}
		} else if msg.Nonce != nil {
			return protocol.ErrProtocol
		}
		if err := s.client.update(s.ctx, func(c *Credentials) error {
			if c.DynamicPairingRounds >= 20 {
				return protocol.ErrProtocol
			}
			c.DynamicPairingRounds++
			return nil
		}); err != nil {
			return err
		}
		p.round++
		p.state = "auth"
		p.displayed = true
		displayCtx, stopDisplay := context.WithDeadline(s.ctx, s.pairingDeadline)
		displayErr := s.client.cfg.CodePairing.Display(displayCtx, PairingDisplay{Active: true, PIN: p.pin, ServerID: s.channel.ServerID, Round: p.round})
		stopDisplay()
		if expired, err := s.checkPairingTimeout(monotime.Now()); err != nil || expired {
			return err
		}
		if displayErr != nil {
			s.endPairing(true)
			return s.send("pair/abort", map[string]string{"reason": "user_cancelled"})
		}
		return nil
	case "server/pair-auth":
		if p.state != "auth" {
			return protocol.ErrProtocol
		}
		var msg struct {
			Share string `json:"pake_msg_1"`
		}
		if err := protocol.Decode(data, &msg, "pake_msg_1"); err != nil {
			return err
		}
		ya, err := codeBytes(msg.Share, 32)
		if err != nil {
			return err
		}
		yb, result, err := cpace.Respond([]byte(p.pin.Value()), p.sid(s.media.pairingIndex), ya)
		if err != nil {
			return protocol.ErrProtocol
		}
		p.pake = result
		p.state = "confirm"
		return s.send("client/pair-auth", map[string]string{"pake_msg_2": base64.RawURLEncoding.EncodeToString(yb)})
	case "server/pair-confirm":
		if p.state != "confirm" || p.pake == nil {
			return protocol.ErrProtocol
		}
		var msg struct {
			Tag string `json:"server_kc"`
		}
		if err := protocol.Decode(data, &msg, "server_kc"); err != nil {
			return err
		}
		tag, err := codeBytes(msg.Tag, 64)
		if err != nil {
			return err
		}
		if !p.pake.Verify(tag) {
			return s.codeMismatch()
		}
		isk, clientTag, err := p.pake.Confirmed()
		if err != nil {
			return protocol.ErrProtocol
		}
		defer clear(isk[:])
		if p.method == "dynamic_pairing_code" {
			if err = s.client.update(s.ctx, func(c *Credentials) error { c.DynamicPairingRounds = 0; return nil }); err != nil {
				return err
			}
		}
		sid := p.sid(s.media.pairingIndex)
		confirm := map[string]string{"client_kc": base64.RawURLEncoding.EncodeToString(clientTag)}
		if p.method == "dynamic_pairing_code" {
			sealed, err := s.wrapCode("sendspin-pair-nonce-wrap-v1", sid, isk[:], p.nonceB)
			if err != nil {
				return err
			}
			confirm["wrapped_nonce_B"] = sealed
		}
		sealed, err := s.wrapCode("sendspin-pair-psk-wrap-v1", sid, isk[:], s.pendingPSK[:])
		if err != nil {
			return err
		}
		if expired, err := s.checkPairingTimeout(monotime.Now()); err != nil || expired {
			return err
		}
		if err = s.send("client/pair-confirm", confirm); err != nil {
			return err
		}
		if expired, err := s.checkPairingTimeout(monotime.Now()); err != nil || expired {
			return err
		}
		p.state = "finalize"
		p.pake.Close()
		p.pake = nil
		return s.send("client/pair-finalize", map[string]string{"wrapped_psk": sealed})
	default:
		return protocol.ErrProtocol
	}
}
func (s *session) codeMismatch() error {
	p := s.codePairing
	if p.method == "dynamic_pairing_code" {
		s.client.mu.Lock()
		count := s.client.credentials.DynamicPairingRounds
		s.client.mu.Unlock()
		if count < 20 {
			p.pake.Close()
			p.pake = nil
			p.state = "retry"
			return s.send("client/pair-retry", map[string]any{})
		}
	} else {
		s.client.mu.Lock()
		w := &s.client.pairWindow
		if w.owner == s {
			w.failures++
			if w.failures >= 5 {
				*w = pairingWindow{}
			}
		}
		s.client.mu.Unlock()
	}
	s.endPairing(true)
	return s.send("pair/abort", map[string]string{"reason": "pairing_code_mismatch"})
}
func (s *session) wrapCode(label string, sid, isk, value []byte) (string, error) {
	input := append(append([]byte(label), sid...), isk...)
	key := sha256.Sum256(input)
	clear(input)
	defer clear(key[:])
	var aead cipher.AEAD
	var err error
	if s.client.cfg.CipherSuite == wire.AES {
		block, e := aes.NewCipher(key[:])
		if e != nil {
			return "", e
		}
		aead, err = cipher.NewGCM(block)
	} else {
		aead, err = chacha20poly1305.New(key[:])
	}
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nil, make([]byte, 12), value, nil)), nil
}
