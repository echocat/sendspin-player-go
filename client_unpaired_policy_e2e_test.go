//go:build e2e

package sendspin

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestE2EReferenceUnpairedPolicyIsAdvertisedAndNotRemotelyMutable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			credentials, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			ref := startReferenceWithOptions(t, credentials, credentials.PairingPSK, false, map[string]any{"no_record": true, "observe_core": true})
			store := &controlCountingStore{}
			if err = store.Save(context.Background(), credentials); err != nil {
				t.Fatal(err)
			}
			client, err := New(context.Background(), Config{Name: "Immutable unpaired policy", Store: store, Roles: []Role{Controller}, AllowUnpaired: enabled})
			if err != nil {
				t.Fatal(err)
			}
			connection := connectReference(t, client, ref)
			eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
			hello := corePeerEvents(t, ref, "core_hello")
			if len(hello) != 1 || hello[0]["unpaired_access_enabled"] != enabled {
				t.Fatal("actual client/hello policy differs from the configured boolean")
			}
			before := store.Saves.Load()
			// Deliberate malformed-policy-peer extension injection. The strict
			// native peer still owns Noise; no runtime toggle exists in the SDK.
			remotePolicy := map[string]any{"enabled": !enabled}
			sendPairingFixtureMessage(t, ref, "server/state", map[string]any{"unpaired_access": remotePolicy})
			sendPairingFixtureMessage(t, ref, "server/command", map[string]any{"unpaired_access": remotePolicy})
			pairingFixtureBarrier(t, client, ref, "remote-policy-fields-processed")
			if store.Saves.Load() != before {
				t.Fatal("remote policy extension wrote credentials")
			}
			if enabled {
				// Dedicated native operator approval remains required separately
				// from the client's advertised/immutable local permission.
				ref.control(t, map[string]any{"command": "trust_unpaired"})
				eventually(t, func() bool { return client.Snapshot().Available && len(client.Snapshot().ActiveRoles) > 0 })
				pairingFixtureBarrier(t, client, ref, "local-policy-still-enabled")
			} else {
				// Explicit unapproved-peer fault: local access denial must still
				// reject the otherwise well-typed playback activation.
				sendPairingFixtureMessage(t, ref, "server/activate", map[string]any{"activities": []string{"playback"}, "active_roles": []string{"controller@v1"}})
				select {
				case <-connection.done:
				case <-time.After(3 * time.Second):
					t.Fatal("remote extension enabled unpaired playback")
				}
				var goodbye *GoodbyeError
				if !errors.As(connection.err, &goodbye) || goodbye.Reason != "pairing_required" {
					t.Fatal("local policy denial used the wrong response", connection.err)
				}
				eventually(t, func() bool {
					return referenceGoodbyeObserved(t, ref, "pairing_required") && referenceEventCount(t, ref, "disconnected", "") == 1
				})
			}
			stored, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if store.Saves.Load() != before || !reflect.DeepEqual(stored, credentials) {
				t.Fatal("unpaired policy test changed identity, pairing records or history")
			}
		})
	}
}
