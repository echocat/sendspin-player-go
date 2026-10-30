//go:build e2e

package sendspin

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func corePeerEvents(t *testing.T, ref referenceServer, kind string) []map[string]any {
	t.Helper()
	var matches []map[string]any
	for _, raw := range ref.control(t, map[string]any{"command": "status"})["events"].([]any) {
		event := raw.(map[string]any)
		if event["type"] == kind {
			matches = append(matches, event)
		}
	}
	return matches
}

func TestE2EReferenceCoreClockAndLeaveDoNotRequireAPlayerRole(t *testing.T) {
	for _, withController := range []bool{false, true} {
		name := "no-active-roles"
		roles := []Role{}
		if withController {
			name = "controller-only"
			roles = []Role{Controller}
		}
		t.Run(name, func(t *testing.T) {
			credentials, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			psk := Secret{113}
			ref := startReferenceWithOptions(t, credentials, psk, false, map[string]any{"observe_core": true})
			credentials.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
			store := &controlCountingStore{}
			if err = store.Save(context.Background(), credentials); err != nil {
				t.Fatal(err)
			}
			client, err := New(context.Background(), Config{Name: "Core-only interoperability", Store: store, Roles: roles})
			if err != nil {
				t.Fatal(err)
			}
			connection := connectReference(t, client, ref)
			eventually(t, func() bool {
				return client.Snapshot().ServerID == ref.id && len(corePeerEvents(t, ref, "clock_request")) >= 3
			})
			hello := corePeerEvents(t, ref, "core_hello")
			if len(hello) != 1 || hello[0]["psk_descriptor_empty"] != true {
				t.Fatal("missing/extraneous pairing descriptor")
			}
			methods, ok := hello[0]["methods"].([]any)
			if !ok || len(methods) != 1 || methods[0] != "pairing_psk" {
				t.Fatal("advertised an unimplemented pairing method")
			}
			offered, ok := hello[0]["roles"].([]any)
			if !ok || len(offered) != len(roles) || (withController && offered[0] != string(Controller)) {
				t.Fatal("hello did not preserve the declared role subset")
			}
			var previous int64
			for _, event := range corePeerEvents(t, ref, "clock_request") {
				text, ok := event["client_transmitted"].(string)
				if !ok || event["unexpected_keys"] != float64(0) {
					t.Fatal("malformed/extraneous clock-request fields")
				}
				at, err := strconv.ParseInt(text, 10, 64)
				if err != nil || at <= previous {
					t.Fatal("clock requests did not use increasing integer microseconds", err)
				}
				previous = at
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			before := store.Saves.Load()
			sendPairingFixtureMessage(t, ref, "server/command", map[string]any{"source": map[string]any{"command": false, "future": nil}, "_unimplemented": false})
			pairingFixtureBarrier(t, client, ref, "opaque-source-command-ignored")
			if err = client.LeaveGroup(ctx); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { return len(corePeerEvents(t, ref, "leave_request")) == 1 })
			if corePeerEvents(t, ref, "leave_request")[0]["unexpected_keys"] != float64(0) {
				t.Fatal("client/leave carried undefined fields")
			}
			pairingFixtureBarrier(t, client, ref, "after-core-leave")
			if store.Saves.Load() != before {
				t.Fatal("core leave changed stored credentials")
			}
			select {
			case <-connection.done:
				t.Fatal("core leave closed the connection", connection.err)
			default:
			}
		})
	}
}

func TestE2EReferenceMalformedCoreUpdatesCloseSilently(t *testing.T) {
	for _, test := range []struct {
		name, kind string
		payload    map[string]any
	}{
		{"group-missing-name", "group/update", map[string]any{"group_id": "g", "playback_state": "playing"}},
		{"group-null-id", "group/update", map[string]any{"group_id": nil, "group_name": "g", "playback_state": "stopped"}},
		{"group-name-array", "group/update", map[string]any{"group_id": "g", "group_name": []any{}, "playback_state": "playing"}},
		{"group-invalid-state", "group/update", map[string]any{"group_id": "g", "group_name": "g", "playback_state": "paused"}},
		{"group-case-alias", "group/update", map[string]any{"GROUP_ID": "g", "group_name": "g", "playback_state": "stopped"}},
		{"time-missing-field", "server/time", map[string]any{"client_transmitted": 0, "server_received": 1}},
		{"time-null-field", "server/time", map[string]any{"client_transmitted": 0, "server_received": nil, "server_transmitted": 2}},
		{"time-wrong-field", "server/time", map[string]any{"client_transmitted": 0, "server_received": 1, "server_transmitted": "2"}},
		{"source-command-null", "server/command", map[string]any{"source": nil}},
		{"source-command-array", "server/command", map[string]any{"source": []any{}}},
	} {
		for _, available := range []bool{false, true} {
			name := test.name + "/available"
			if !available {
				name = test.name + "/unavailable"
			}
			t.Run(name, func(t *testing.T) {
				credentials, err := NewCredentials()
				if err != nil {
					t.Fatal(err)
				}
				psk := Secret{114}
				ref := startReference(t, credentials, psk, false)
				credentials.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
				store := &controlCountingStore{}
				if err = store.Save(context.Background(), credentials); err != nil {
					t.Fatal(err)
				}
				client, err := New(context.Background(), Config{Name: "Malformed core update", Store: store, Roles: []Role{Controller}})
				if err != nil {
					t.Fatal(err)
				}
				connection := connectReference(t, client, ref)
				eventually(t, func() bool { return client.Snapshot().Available })
				if !available {
					if err = client.SetAvailable(context.Background(), false); err != nil {
						t.Fatal(err)
					}
				}
				before := store.Saves.Load()
				sendPairingFixtureMessage(t, ref, test.kind, test.payload)
				requireSilentProtocolClose(t, connection, ref)
				if store.Saves.Load() != before {
					t.Fatal("malformed core update persisted data")
				}
			})
		}
	}
}

func TestE2EReferenceUnpairedUnpairDoesNotDeleteUnrelatedTrust(t *testing.T) {
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	previous := PairingRecord{ServerID: unrelated.ClientID(), PSK: Secret{115}}
	credentials.Records = []PairingRecord{previous}
	ref := startReferenceWithOptions(t, credentials, credentials.PairingPSK, false, map[string]any{"no_record": true})
	store := &controlCountingStore{}
	if err = store.Save(context.Background(), credentials); err != nil {
		t.Fatal(err)
	}
	client, err := New(context.Background(), Config{Name: "Unpaired unpair", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	before := store.Saves.Load()
	sendPairingFixtureMessage(t, ref, "server/unpair", map[string]any{})
	pairingFixtureBarrier(t, client, ref, "unpaired-unpair-processed")
	stored, err := store.Load(context.Background())
	if err != nil || len(stored.Records) != 1 || stored.Records[0] != previous || stored.PrivateKey != credentials.PrivateKey || stored.PairingPSK != credentials.PairingPSK || store.Saves.Load() != before {
		t.Fatal("unpaired unpair changed identity or stored trust", err)
	}
	select {
	case <-connection.done:
		t.Fatal("unpaired unpair closed the connection", connection.err)
	default:
	}
}
