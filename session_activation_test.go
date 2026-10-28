package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func idleActivationSession(t *testing.T) *session {
	t.Helper()
	client, err := New(context.Background(), Config{Name: "Activation fields", Store: &MemoryStore{}, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, context.Background(), func() {})
	s.channel = &wire.Channel{ServerID: client.ClientID(), Category: "pr"}
	s.hello, s.activated, s.admitted = true, true, true
	return s
}

func TestActivationIgnoresPairingFieldWithoutPairingActivity(t *testing.T) {
	for _, raw := range []string{`null`, `false`, `42`, `"unused"`, `[]`, `{}`, `{"method":null,"format":false}`, `{"method":"dynamic_pairing_code","format":"future"}`} {
		t.Run(raw, func(t *testing.T) {
			s := idleActivationSession(t)
			if err := s.activate([]byte(`{"activities":[],"pairing":` + raw + `}`)); err != nil {
				t.Fatal("field required to be ignored was validated", err)
			}
			if s.pendingPSK != nil || s.pairingAttempt || s.media.pairingIndex != 0 || !s.activated {
				t.Fatal("ignored pairing field changed pairing state")
			}
		})
	}
}

func TestActivationRejectsMalformedPairingBeforeAnyWrite(t *testing.T) {
	for _, field := range []string{``, `,"pairing":null`, `,"pairing":[]`, `,"pairing":{}`, `,"pairing":{"method":null}`, `,"pairing":{"method":4}`, `,"pairing":{"method":"pairing_psk","format":null}`, `,"pairing":{"method":"pairing_psk","format":4}`, `,"pairing":{"method":"dynamic_pairing_code"}`} {
		t.Run(field, func(t *testing.T) {
			s := idleActivationSession(t)
			// No transport keys/socket exist: protocol failures must not write.
			if err := s.activate([]byte(`{"activities":["pairing"]` + field + `}`)); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("malformed pairing did not fail silently", err)
			}
			if s.pendingPSK != nil || s.pairingAttempt || s.media.pairingIndex != 0 {
				t.Fatal("malformed pairing started or counted an attempt")
			}
		})
	}
}

func TestNextActivationEndsPairingDiscardWindow(t *testing.T) {
	for _, pending := range []bool{false, true} {
		s := idleActivationSession(t)
		s.pairingDiscard = !pending
		if pending {
			psk := Secret{19}
			s.pendingPSK = &psk
			s.setPairingAttempt(true)
		}
		if err := s.activate([]byte(`{"activities":[]}`)); err != nil {
			t.Fatal(err)
		}
		if s.pairingDiscard || s.pendingPSK != nil || s.pairingAttempt {
			t.Fatal("activation did not terminate the previous attempt/discard window")
		}
		if err := s.dispatch(append([]byte{0}, []byte(`{"type":"server/pair-finalize","payload":{}}`)...)); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("post-barrier finalize was incorrectly treated as stale", err)
		}
	}
}

func TestPairingSequenceChecksRespectDiscardBoundary(t *testing.T) {
	for _, kind := range []string{"server/pair-init", "server/pair-auth", "server/pair-confirm", "client/pair-pending", "client/pair-auth", "client/pair-confirm", "client/pair-retry"} {
		t.Run(kind, func(t *testing.T) {
			s := idleActivationSession(t)
			frame, err := protocol.Encode(kind, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			frame = append([]byte{0}, frame...)
			if err := s.dispatch(frame); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("recognized out-of-sequence/wrong-direction message was ignored", err)
			}
		})
	}
	for _, kind := range []string{"server/pair-init", "server/pair-auth", "server/pair-confirm", "server/pair-finalize"} {
		s := idleActivationSession(t)
		s.pairingDiscard = true
		frame, _ := protocol.Encode(kind, map[string]any{"future": nil})
		if err := s.dispatch(append([]byte{0}, frame...)); err != nil {
			t.Fatal("in-flight message was validated during discard window", kind, err)
		}
	}
}

func TestAbortAfterCompletedAttemptHasNoEffect(t *testing.T) {
	s := idleActivationSession(t)
	if err := s.dispatch(append([]byte{0}, []byte(`{"type":"pair/abort","payload":{"reason":"user_cancelled"}}`)...)); err != nil {
		t.Fatal(err)
	}
	if s.pairingDiscard {
		t.Fatal("late abort started a new discard window")
	}
}

func TestRemoteAbortAbandonsPendingPairingWithoutPersistence(t *testing.T) {
	s := idleActivationSession(t)
	psk := Secret{21}
	s.pendingPSK = &psk
	s.pairingDeadline = time.Now().Add(time.Minute)
	s.setPairingAttempt(true)
	frame := append([]byte{0}, []byte(`{"type":"pair/abort","payload":{"reason":"user_cancelled"}}`)...)
	if err := s.dispatch(frame); err != nil {
		t.Fatal(err)
	}
	if s.pendingPSK != nil || !s.pairingDeadline.IsZero() || s.pairingAttempt || !s.pairingDiscard {
		t.Fatal("remote abort retained a pending credential/attempt")
	}
	if err := s.dispatch(append([]byte{0}, []byte(`{"type":"server/pair-finalize","payload":{}}`)...)); err != nil {
		t.Fatal("stale finalize was not discarded", err)
	}
	cred, err := s.client.cfg.Store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cred.Records) != 0 {
		t.Fatal("aborted attempt persisted a record")
	}
}

func TestPairingIndexCannotWrap(t *testing.T) {
	s := idleActivationSession(t)
	s.media.pairingIndex = ^uint32(0)
	if err := s.activate([]byte(`{"activities":["pairing"],"pairing":{"method":"pairing_psk"}}`)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("exhausted index was reused", err)
	}
	if s.media.pairingIndex != ^uint32(0) || s.pendingPSK != nil || s.pairingAttempt {
		t.Fatal("exhausted counter wrapped or started an attempt")
	}
}

func TestRejectedPairingActivationCannotRetainOldPermissions(t *testing.T) {
	for _, playbackCapable := range []bool{false, true} {
		s := idleActivationSession(t)
		s.admitted = false
		s.awaitingActivation = true
		s.active = map[string]Role{"controller": Controller, "metadata": MetadataRole}
		s.state.ActiveRoles = []Role{Controller, MetadataRole}
		s.state.Controller = &ControllerState{}
		s.state.Metadata = &Metadata{Title: "old"}
		s.pendingMetadata = &Metadata{Title: "future"}
		s.state.Available = true
		psk := Secret{77}
		s.pendingPSK = &psk
		s.pairingDeadline = time.Now().Add(time.Minute)
		s.setPairingAttempt(true)
		if err := s.rejectPairingActivation(playbackCapable); err != nil {
			t.Fatal(err)
		}
		if s.admitted || s.awaitingActivation || !s.activated || s.pendingPSK != nil || !s.pairingDeadline.IsZero() || s.pairingAttempt || !s.pairingDiscard {
			t.Fatal("refusal retained pairing state or admitted the connection")
		}
		if playbackCapable {
			if len(s.active) != 2 || s.state.Metadata == nil || s.pendingMetadata == nil || s.state.Controller == nil || !s.state.Available {
				t.Fatal("rejected activation changed still-authorized roles")
			}
		} else if len(s.active) != 0 || len(s.state.ActiveRoles) != 0 || s.state.Metadata != nil || s.pendingMetadata != nil || s.state.Controller != nil || s.state.Available {
			t.Fatal("old-key permissions survived refusal under an unpaired key")
		}
	}
}

func TestRejectedPairingActivationStillRequiresStreamEnd(t *testing.T) {
	s := idleActivationSession(t)
	s.active = map[string]Role{"artwork": ArtworkRole}
	s.state.ActiveRoles = []Role{ArtworkRole}
	s.streams["artwork"] = true
	s.awaitingActivation = true
	if err := s.rejectPairingActivation(false); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("active stream removed without required end", err)
	}
	if !s.awaitingActivation {
		t.Fatal("invalid removal released the application-write barrier")
	}
}

func TestActivationRefusalOrder(t *testing.T) {
	for _, tc := range []struct {
		name, category, want                       string
		allow, playback, pairing, roles, supported bool
	}{
		{name: "paired-idle", category: "lt", supported: true},
		{name: "paired-playback", category: "lt", playback: true, roles: true, supported: true},
		{name: "paired-pairing", category: "lt", pairing: true, supported: false, want: "unauthorized"},
		{name: "paired-both", category: "lt", pairing: true, playback: true, supported: false, want: "unauthorized"},
		{name: "sentinel-idle", category: "sn", supported: true},
		{name: "sentinel-idle-roles", category: "sn", roles: true, supported: true, want: "pairing_required"},
		{name: "sentinel-playback", category: "sn", playback: true, supported: true, want: "pairing_required"},
		{name: "sentinel-approved", category: "sn", allow: true, playback: true, roles: true, supported: true},
		{name: "sentinel-unsupported-method", category: "sn", pairing: true, want: "method_not_supported"},
		{name: "sentinel-both-unsupported", category: "sn", pairing: true, playback: true, want: "unauthorized"},
		{name: "sentinel-both-approved-unsupported", category: "sn", allow: true, pairing: true, playback: true, want: "method_not_supported"},
		{name: "psk-pair", category: "pr", pairing: true, supported: true},
		{name: "psk-both", category: "pr", pairing: true, playback: true, supported: true, want: "pairing_required"},
		{name: "psk-both-unoffered-format", category: "pr", pairing: true, playback: true, want: "unauthorized"},
		{name: "psk-unoffered-format", category: "pr", pairing: true, want: "method_not_supported"},
		{name: "psk-both-approved", category: "pr", allow: true, pairing: true, playback: true, roles: true, supported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := idleActivationSession(t)
			s.channel.Category = tc.category
			s.client.cfg.AllowUnpaired = tc.allow
			var roles []Role
			if tc.roles {
				roles = []Role{Controller}
			}
			got := s.activationRefusal(map[string]bool{"playback": tc.playback, "pairing": tc.pairing}, roles, tc.supported)
			if got != tc.want {
				t.Fatalf("refusal %q, want %q", got, tc.want)
			}
		})
	}
}

func FuzzActivationIgnoredPairingField(f *testing.F) {
	for _, raw := range []string{`null`, `false`, `0`, `"unused"`, `[]`, `{}`, `{"method":null,"format":[1,true]}`} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 128<<10 || !utf8.Valid(raw) || !json.Valid(raw) {
			return
		}
		client := &Client{}
		s := newSession(client, context.Background(), func() {})
		s.channel = &wire.Channel{ServerID: "fuzz", Category: "pr"}
		s.hello, s.activated, s.admitted = true, true, true
		data := append([]byte(`{"activities":[],"pairing":`), raw...)
		data = append(data, '}')
		if !json.Valid(data) {
			return
		} // Adding the outer object can cross JSON's depth limit.
		if err := s.activate(data); err != nil {
			t.Fatal("ignored pairing value was validated", err)
		}
		if s.pendingPSK != nil || s.pairingAttempt || s.media.pairingIndex != 0 {
			t.Fatal("ignored value started pairing")
		}
	})
}
