package sendspin

import (
	"errors"
	"time"
)

var errPairingDeadline = errors.New("sendspin: pairing deadline expired before persistence")

func (s *session) endPairing(discard bool) {
	s.pendingPSK = nil
	s.pairingDeadline = time.Time{}
	s.pairingDiscard = discard
	s.setPairingAttempt(false)
}

// expirePairing makes the deadline inclusive and releases all attempt state at
// once. A re-handshake's application-write gate must not be bypassed: its next
// activation supersedes the old attempt instead of aborting a new one by mistake.
func (s *session) expirePairing(at time.Time) bool {
	if s.awaitingActivation || s.pendingPSK == nil || s.pairingDeadline.IsZero() || at.Before(s.pairingDeadline) {
		return false
	}
	s.endPairing(true)
	return true
}

func (s *session) checkPairingTimeout(at time.Time) (bool, error) {
	if !s.expirePairing(at) {
		return false, nil
	}
	return true, s.send("pair/abort", map[string]string{"reason": "attempt_timeout"})
}
