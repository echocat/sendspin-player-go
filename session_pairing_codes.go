package sendspin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
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
	if s.client.automaticPairing() {
		c := s.client
		c.mu.Lock()
		if c.pairingPINOwner == s {
			c.pairingPIN, c.pairingPINOwner = PairingPINDisplay{}, nil
		}
		if s.pairingCurrentLocked() && c.credentials.PairingAuthPending {
			// An unanswered confirmation is NOT a proven wrong PIN. Its
			// durable reservation conservatively delays another attempt.
			c.pairingCooldownUntil = pairingNow().Add(pairingCooldown)
		}
		c.pairingChangedLocked()
		c.mu.Unlock()
	}
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
		if c.automaticPairing() {
			return s.pairingCurrentLocked() && c.pairingStateLocked().Ready
		}
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

// Counter writes are part of the same attempt budget as authentication and
// final persistence. Waiting for Client.mu or a context-cooperative Store must
// not extend an attempt, and a late-returning Store must not trigger display or
// credential output. Store-owned/earlier parent deadlines remain distinct.
func (s *session) updatePairingCounter(update func(*Credentials) error) (bool, error) {
	if expired, err := s.checkPairingTimeout(monotime.Now()); expired || err != nil {
		return expired, err
	}
	ctx := s.ctx
	if !s.pairingDeadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, s.pairingDeadline)
		defer cancel()
	}
	err := s.update(ctx, func(c *Credentials) error {
		if !s.pairingDeadline.IsZero() && !monotime.Now().Before(s.pairingDeadline) {
			return errPairingDeadline
		}
		return update(c)
	})
	if err != nil {
		if (errors.Is(err, errPairingDeadline) || errors.Is(err, context.DeadlineExceeded)) && !s.pairingDeadline.IsZero() && !monotime.Now().Before(s.pairingDeadline) {
			return s.checkPairingTimeout(monotime.Now())
		}
		if s.client.automaticPairing() && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrPairingRestarting) {
			s.client.mu.Lock()
			if s.pairingCurrentLocked() {
				s.client.pairingBlocked = true
				s.client.pairingPIN = PairingPINDisplay{}
				s.client.pairingChangedLocked()
			}
			s.client.mu.Unlock()
		}
		return false, err
	}
	return s.checkPairingTimeout(monotime.Now())
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
		if expired, err := s.updatePairingCounter(func(c *Credentials) error {
			if c.DynamicPairingRounds >= 20 {
				return protocol.ErrProtocol
			}
			if s.client.automaticPairing() {
				if len(c.Records) > 0 || s.client.pairingPaused || s.client.pairingBlocked || pairingNow().Before(s.client.pairingCooldownUntil) {
					return ErrPairingActive
				}
				c.PairingCooldown, c.PairingAuthPending = false, false
			}
			c.DynamicPairingRounds++
			return nil
		}); expired || err != nil {
			return err
		}
		p.round++
		p.state = "auth"
		if s.client.automaticPairing() {
			s.client.mu.Lock()
			if !s.pairingCurrentLocked() || s.ctx.Err() != nil {
				s.client.mu.Unlock()
				return ErrPairingRestarting
			}
			s.client.pairingPIN = PairingPINDisplay{PIN: p.pin, ValidUntil: s.pairingDeadline}
			s.client.pairingPINOwner = s
			s.client.pairingChangedLocked()
			s.client.mu.Unlock()
		}
		if s.client.cfg.CodePairing.Display == nil {
			return nil
		}
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
		if s.client.automaticPairing() {
			// Persist before emitting a response that permits a password guess.
			// A crash between verification and its write cannot erase protection.
			if expired, err := s.updatePairingCounter(func(c *Credentials) error {
				c.PairingAuthPending = true
				return nil
			}); expired || err != nil {
				result.Close()
				return err
			}
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
			if expired, storeErr := s.updatePairingCounter(func(c *Credentials) error {
				c.DynamicPairingRounds = 0
				if s.client.automaticPairing() {
					c.PairingAuthPending = false
					s.client.pairingPIN = PairingPINDisplay{}
				}
				return nil
			}); expired || storeErr != nil {
				return storeErr
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
	if s.client.automaticPairing() {
		cooldown := false
		if expired, err := s.updatePairingCounter(func(c *Credentials) error {
			c.PairingAuthPending = false
			c.PairingFailures++
			if c.PairingFailures == 5 {
				c.PairingFailures = 0
				c.PairingCooldown = true
				cooldown = true
			}
			return nil
		}); expired || err != nil {
			return err
		}
		if cooldown {
			s.client.mu.Lock()
			if s.pairingCurrentLocked() {
				s.client.pairingCooldownUntil = pairingNow().Add(pairingCooldown)
				s.client.pairingChangedLocked()
			}
			s.client.mu.Unlock()
			s.endPairing(true)
			return s.send("pair/abort", map[string]string{"reason": "pairing_code_mismatch"})
		}
	}
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
