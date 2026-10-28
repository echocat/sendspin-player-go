package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func coreFrame(t *testing.T, kind string, payload any) []byte {
	t.Helper()
	message, err := protocol.Encode(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte{0}, message...)
}

func TestCoreRequestsRespectInitialAndRehandshakeGates(t *testing.T) {
	for _, phase := range []string{"before-hello", "before-activation", "rehandshake"} {
		s := idleActivationSession(t)
		s.hello = phase != "before-hello"
		s.activated = phase == "rehandshake"
		s.awaitingActivation = phase == "rehandshake"
		// No socket/keys exist: an early client/leave write would panic.
		if err := s.request(request{ctx: context.Background(), kind: "leave"}); err == nil {
			t.Fatal("core request bypassed message sequencing", phase)
		}
	}
}

func TestCoreGroupUpdateRequiresFullTypedStateAndReplacesIt(t *testing.T) {
	for _, available := range []bool{false, true} {
		for _, field := range []string{"group_id", "group_name", "playback_state"} {
			for _, bad := range []string{"missing", `null`, `false`, `1`, `[]`, `{}`} {
				s := idleActivationSession(t)
				s.state.Available = available
				before := Group{ID: "keep-id", Name: "keep-name", PlaybackState: "playing"}
				s.state.Group = before
				payload := map[string]json.RawMessage{"group_id": json.RawMessage(`"new"`), "group_name": json.RawMessage(`"new"`), "playback_state": json.RawMessage(`"stopped"`)}
				if bad == "missing" {
					delete(payload, field)
				} else {
					payload[field] = json.RawMessage(bad)
				}
				if err := s.dispatch(coreFrame(t, "group/update", payload)); !errors.Is(err, protocol.ErrProtocol) {
					t.Fatal("incomplete/malformed group state accepted", available, field, bad, err)
				}
				if s.state.Group != before || s.state.Available != available {
					t.Fatal("failed group update mutated prior state")
				}
			}
		}
		for _, state := range []string{"paused", "Playing", "", "future"} {
			s := idleActivationSession(t)
			if err := s.dispatch(coreFrame(t, "group/update", map[string]any{"group_id": "g", "group_name": "n", "playback_state": state})); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("invalid group playback state accepted", state, err)
			}
		}
		s := idleActivationSession(t)
		s.client.active = s
		s.state.Available = available
		for _, state := range []string{"playing", "stopped"} {
			// Empty IDs/names are strings, not an invented invalid-ID restriction.
			if err := s.dispatch(coreFrame(t, "group/update", map[string]any{"group_id": "", "group_name": "", "playback_state": state, "GROUP_ID": nil, "future": []any{nil}})); err != nil {
				t.Fatal(err)
			}
			if got := s.client.Snapshot().Group; got != (Group{PlaybackState: state}) {
				t.Fatal("group state was merged or lost in the public snapshot", got)
			}
			if s.state.Available != available {
				t.Fatal("group update changed local availability")
			}
		}
	}
}

func TestCoreTimeResponseSchemaAndCorrelation(t *testing.T) {
	for _, field := range []string{"client_transmitted", "server_received", "server_transmitted"} {
		for _, bad := range []string{"missing", `null`, `false`, `"100"`, `1.5`, `[]`, `{}`, `9223372036854775808`} {
			for _, matches := range []bool{false, true} {
				s := idleActivationSession(t)
				s.arrival = 900
				s.timeOutstanding = 100
				payload := map[string]json.RawMessage{"client_transmitted": json.RawMessage(`100`), "server_received": json.RawMessage(`500`), "server_transmitted": json.RawMessage(`600`)}
				if !matches {
					payload["client_transmitted"] = json.RawMessage(`101`)
				}
				if bad == "missing" {
					delete(payload, field)
				} else {
					payload[field] = json.RawMessage(bad)
				}
				if err := s.dispatch(coreFrame(t, "server/time", payload)); !errors.Is(err, protocol.ErrProtocol) {
					t.Fatal("known malformed clock field bypassed validation", field, bad, matches, err)
				}
				if s.timeOutstanding != 100 || s.burstLeft != 8 || s.bestAt != 0 || s.filter.Ready() {
					t.Fatal("malformed clock response changed synchronization state")
				}
			}
		}
	}
	s := idleActivationSession(t)
	s.arrival = 900
	s.timeOutstanding = 100
	for _, echo := range []int64{0, 99, 101} {
		if err := s.dispatch(coreFrame(t, "server/time", map[string]any{"client_transmitted": echo, "server_received": 500, "server_transmitted": 600})); err != nil {
			t.Fatal(err)
		}
		if s.timeOutstanding != 100 || s.burstLeft != 8 || s.bestAt != 0 {
			t.Fatal("unrelated response consumed the active request")
		}
	}
	if err := s.dispatch(coreFrame(t, "server/time", map[string]any{"client_transmitted": 100, "server_received": 500, "server_transmitted": 600, "CLIENT_TRANSMITTED": nil, "future": []any{false}})); err != nil {
		t.Fatal(err)
	}
	if s.timeOutstanding != 0 || s.burstLeft != 7 || s.bestAt != 900 || s.filter.ServerTime(1000) != 1050 {
		t.Fatal("matching clock response did not update only its request")
	}
}

func TestUnsupportedSourceCommandStillRequiresAnOuterObject(t *testing.T) {
	for _, raw := range []string{`null`, `false`, `1`, `[]`, `"opaque"`} {
		s, store := controlSession(t)
		before := store.Saves.Load()
		payload := []byte(`{"source":` + raw + `,"player":{"command":"volume","volume":37}}`)
		if err := s.serverCommand(payload); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("known source field accepted a non-object", raw, err)
		}
		if store.Saves.Load() != before || s.state.Volume != 100 {
			t.Fatal("known malformed outer field allowed a partial player-command mutation")
		}
	}
	for _, raw := range []string{`{}`, `{"command":false,"opaque":null}`, `{"future":[],"new":{}}`} {
		s := idleActivationSession(t)
		if err := s.serverCommand([]byte(`{"source":` + raw + `,"_unimplemented":false}`)); err != nil {
			t.Fatal("unsupported role internals were interpreted", raw, err)
		}
	}
}

func TestClockDomainsDoNotAssumeWallClockEpoch(t *testing.T) {
	for _, origin := range []int64{0, 1_000_000_000_000, -1_000_000_000_000} {
		s := idleActivationSession(t)
		s.arrival = 900
		s.timeOutstanding = 100
		payload := map[string]int64{"client_transmitted": 100, "server_received": origin + 500, "server_transmitted": origin + 600}
		if err := s.dispatch(coreFrame(t, "server/time", payload)); err != nil {
			t.Fatal(err)
		}
		if s.filter.ServerTime(1000) != origin+1050 || s.filter.ClientTime(origin+1050) != 1000 {
			t.Fatal("clock mapping depended on a shared epoch", origin)
		}
	}
}

func TestRC1BinaryIDRegistryKeepsInactiveIDsRecognized(t *testing.T) {
	implemented := map[byte]bool{0: true, 4: true, 8: true, 9: true, 10: true, 11: true, 16: true, 17: true, 18: true, 19: true, 20: true}
	for id := range 256 {
		if id == 1 {
			continue
		} // Transport reassembly consumes ID 1 and rejects nesting.
		for _, phase := range []string{"initial", "active", "rehandshake"} {
			s := idleActivationSession(t)
			s.hello = phase != "initial"
			s.activated = phase != "initial"
			s.awaitingActivation = phase == "rehandshake"
			err := s.dispatch([]byte{byte(id)}) // Deliberately malformed known payload.
			wantError := phase != "active" || implemented[byte(id)]
			if wantError && !errors.Is(err, protocol.ErrProtocol) || !wantError && err != nil {
				t.Fatal("wrong RC1 discriminator/activation behavior", fmt.Sprintf("0x%02x", id), phase, err)
			}
		}
	}
}

func TestPairAbortReasonsRespectSenderAndEndedAttempt(t *testing.T) {
	for _, payload := range []map[string]any{
		{"reason": "user_cancelled"}, {"reason": "pairing_code_mismatch"}, {"reason": "attempt_timeout"}, {"reason": "concurrent_attempt"}, {"reason": "method_not_supported"}, {"reason": "future"}, {"reason": ""}, {"reason": nil}, {"reason": 1}, {"reason": false}, {"reason": []any{}}, {},
	} {
		for _, pending := range []bool{false, true} {
			s := idleActivationSession(t)
			if pending {
				key := Secret{13}
				s.pendingPSK = &key
				s.setPairingAttempt(true)
			}
			err := s.dispatch(coreFrame(t, "pair/abort", payload))
			reason, _ := payload["reason"].(string)
			allowed := reason == "user_cancelled" || reason == "pairing_code_mismatch"
			if pending && !allowed {
				if !errors.Is(err, protocol.ErrProtocol) || s.pendingPSK == nil || !s.pairingAttempt {
					t.Fatal("invalid sender reason changed a live attempt", payload, err)
				}
				continue
			}
			if err != nil || s.pendingPSK != nil || s.pairingAttempt || s.pairingDiscard != pending {
				t.Fatal("abort was echoed, revived or retained an ended attempt", payload, pending, err)
			}
			credentials, err := s.client.cfg.Store.Load(context.Background())
			if err != nil || len(credentials.Records) != 0 {
				t.Fatal("abort persisted credentials", err)
			}
		}
	}
}
