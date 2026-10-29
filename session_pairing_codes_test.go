package sendspin

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func TestCodePairingRejectsBadSharesAndOutOfSequenceFinalize(t *testing.T) {
	for _, test := range []struct {
		kind, payload string
		state         string
	}{
		{"server/pair-auth", `{"pake_msg_1":""}`, "auth"},
		{"server/pair-auth", `{"pake_msg_1":null}`, "auth"},
		{"server/pair-auth", `{"pake_msg_1":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 32)) + `"}`, "auth"},
		{"server/pair-auth", `{}`, "confirm"},
		{"server/pair-confirm", `{}`, "auth"},
		{"server/pair-init", `{}`, "auth"},
	} {
		t.Run(test.kind+test.state+test.payload, func(t *testing.T) {
			s, _ := newSchemaSession(t, Config{Name: "PIN sequence", Roles: []Role{}, CodePairing: &CodePairingConfig{Display: func(context.Context, PairingDisplay) error { return nil }}})
			s.hello, s.activated = true, true
			s.channel.Category = "sn"
			psk := Secret{9}
			s.pendingPSK = &psk
			s.pairingDeadline = time.Now().Add(time.Minute)
			s.codePairing = &codeAttempt{method: "dynamic_pairing_code", state: test.state, pin: PairingPIN{"123456"}, h: s.channel.BindingHash(), round: 1}
			if err := s.codeMessage(test.kind, []byte(test.payload)); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("bad PIN sequence accepted", err)
			}
			if err := s.finalizePairing([]byte(`{}`)); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("unconfirmed PIN persisted", err)
			}
			s.endPairing(true)
		})
	}
}
func TestCodeAttemptTimeoutDiscardsSecretAndSuppressesInFlightMessages(t *testing.T) {
	s, p := newSchemaSession(t, Config{Name: "PIN timeout", Roles: []Role{}, CodePairing: &CodePairingConfig{Display: func(context.Context, PairingDisplay) error { return nil }}})
	s.hello, s.activated = true, true
	psk := Secret{8}
	s.pendingPSK = &psk
	s.pairingDeadline = time.Now().Add(-time.Second)
	s.codePairing = &codeAttempt{method: "dynamic_pairing_code", state: "auth", nonceB: make([]byte, 32)}
	if expired, err := s.checkPairingTimeout(time.Now()); !expired || err != nil {
		t.Fatal(expired, err)
	}
	expectSchemaPayload(t, p, "pair/abort", map[string]any{"reason": "attempt_timeout"})
	if s.codePairing != nil || s.pendingPSK != nil || !s.pairingDiscard {
		t.Fatal("PIN attempt state survived timeout")
	}
}
