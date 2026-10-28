package sendspin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func TestPairingDeadlineIsInclusiveAndReleasesOnlyAttemptState(t *testing.T) {
	deadline := time.Unix(1700000000, 123456789)
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond, time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			s := idleActivationSession(t)
			psk := Secret{101}
			s.pendingPSK = &psk
			s.pairingDeadline = deadline
			s.setPairingAttempt(true)
			s.state.ActiveRoles = []Role{MetadataRole, ArtworkRole}
			s.active = map[string]Role{"metadata": MetadataRole, "artwork": ArtworkRole}
			s.streams["artwork"] = true
			s.state.Metadata = &Metadata{Title: "keep current"}
			s.pendingMetadata = &Metadata{Title: "keep scheduled"}
			s.state.Group = Group{ID: "keep group"}
			s.state.Available = true
			if expired := s.expirePairing(deadline.Add(offset)); expired != (offset >= 0) {
				t.Fatal("wrong deadline boundary", expired)
			}
			if offset < 0 {
				if s.pendingPSK == nil || !s.pairingAttempt || s.pairingDiscard || !s.pairingDeadline.Equal(deadline) {
					t.Fatal("unexpired attempt changed")
				}
				return
			}
			if s.pendingPSK != nil || s.pairingAttempt || !s.pairingDiscard || !s.pairingDeadline.IsZero() {
				t.Fatal("expired attempt retained state/protection")
			}
			if !s.state.Available || s.state.Metadata.Title != "keep current" || s.pendingMetadata.Title != "keep scheduled" || s.state.Group.ID != "keep group" || s.active["metadata"] != MetadataRole || !s.streams["artwork"] {
				t.Fatal("timeout cleared unrelated role/stream/group state")
			}
			if s.expirePairing(deadline.Add(time.Hour)) {
				t.Fatal("timeout fired more than once")
			}
			for _, kind := range []string{"server/pair-finalize", "server/pair-auth", "pair/abort"} {
				message, _ := protocol.Encode(kind, map[string]any{"future": nil})
				if err := s.dispatch(append([]byte{0}, message...)); err != nil {
					t.Fatal("late in-flight pairing message was not discarded", kind, err)
				}
			}
			persisted, err := s.client.cfg.Store.Load(context.Background())
			if err != nil || len(persisted.Records) != 0 {
				t.Fatal("expired attempt persisted a key", err)
			}
		})
	}
}

func TestPairingDeadlineDoesNotInventAttemptsOrBypassRehandshake(t *testing.T) {
	for _, state := range []string{"not-started", "missing-deadline", "rehandshake"} {
		t.Run(state, func(t *testing.T) {
			s := idleActivationSession(t)
			psk := Secret{102}
			if state != "not-started" {
				s.pendingPSK = &psk
				s.setPairingAttempt(true)
			}
			if state != "missing-deadline" {
				s.pairingDeadline = time.Now().Add(-time.Hour)
			}
			s.awaitingActivation = state == "rehandshake"
			// No socket/transport keys exist. Any application write would panic.
			if expired, err := s.checkPairingTimeout(time.Now()); expired || err != nil {
				t.Fatal("invented an attempt or bypassed the activation gate", expired, err)
			}
			if state == "rehandshake" {
				if s.pendingPSK == nil || !s.pairingAttempt {
					t.Fatal("rehandshake attempt discarded before its activation")
				}
				if err := s.activate([]byte(`{"activities":[]}`)); err != nil {
					t.Fatal(err)
				}
				if s.pendingPSK != nil || s.pairingAttempt || s.pairingDiscard || s.awaitingActivation {
					t.Fatal("new activation did not supersede the old attempt")
				}
				if err := s.finalizePairing([]byte(`{}`)); !errors.Is(err, protocol.ErrProtocol) {
					t.Fatal("post-activation finalize revived the expired attempt", err)
				}
			}
		})
	}
}

func TestExpiredPairingDoesNotMaskUnrelatedSilentProtocolFailures(t *testing.T) {
	for _, frame := range [][]byte{
		append([]byte{0}, []byte(`{"type":"group/update","payload":{"group_id":"x","group_name":"x","playback_state":false}}`)...),
		append([]byte{0}, []byte(`{"type":"noise/handshake","payload":{"data":"AA="}}`)...),
		append([]byte{0}, []byte(`{"type":"server/activate","payload":{}}`)...),
		{4},
	} {
		s := idleActivationSession(t)
		psk := Secret{103}
		s.pendingPSK = &psk
		s.pairingDeadline = time.Now().Add(-time.Hour)
		s.setPairingAttempt(true)
		// No socket/keys are installed. Sending a timeout abort before noticing
		// the unrelated protocol error would panic rather than close silently.
		if err := s.dispatch(frame); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("unrelated protocol failure was hidden", err)
		}
		if s.pendingPSK == nil || !s.pairingAttempt {
			t.Fatal("malformed unrelated message triggered an application abort")
		}
	}
}
