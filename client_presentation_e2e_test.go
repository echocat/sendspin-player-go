//go:build e2e

package sendspin

import (
	"context"
	"math"
	"slices"
	"testing"
)

func referencePresentationClient(t *testing.T) (*Client, referenceServer, *controlCountingStore, *outboundTestConnection) {
	t.Helper()
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{122}
	ref := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &controlCountingStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Presentation fields", Store: store, Roles: []Role{MetadataRole, ColorRole}})
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	return client, ref, store, connection
}

func TestReferencePausedLargePositionIsExactAndProgressCanBeCleared(t *testing.T) {
	client, ref, _, _ := referencePresentationClient(t)
	for _, position := range []int64{9007199254740993, math.MaxInt64 - 1} {
		mediaCommand(t, client, ref, map[string]any{"command": "metadata", "fields": map[string]any{"title": "paused", "track": 1, "track_progress": position, "track_duration": 0, "playback_speed": 0}})
		eventually(t, func() bool { got, known := client.CurrentPosition(); return known && got == position })
		if state := client.Snapshot().Metadata; state.Progress == nil || state.Progress.PositionMS != position || state.Track != 1 {
			t.Fatal("wire progress lost integer precision")
		}
	}
	mediaCommand(t, client, ref, map[string]any{"command": "metadata", "fields": map[string]any{"title": "position unknown"}})
	if _, known := client.CurrentPosition(); known {
		t.Fatal("replacement without progress retained a live position")
	}
	mediaCommand(t, client, ref, map[string]any{"command": "color", "fields": map[string]any{
		"background_dark": []int{0, 0, 0}, "background_light": []int{255, 255, 255}, "primary": []int{0, 255, 0}, "accent": []int{255, 0, 255}, "on_dark": []int{255, 255, 255}, "on_light": []int{0, 0, 0},
	}})
	colors := client.Snapshot().Colors
	if colors == nil || !slices.Equal(colors.BackgroundLight, []int{255, 255, 255}) || !slices.Equal(colors.OnLight, []int{0, 0, 0}) {
		t.Fatal("complete native palette missing", colors)
	}
	colors.OnDark[0] = 0
	if client.Snapshot().Colors.OnDark[0] != 255 {
		t.Fatal("palette read model aliases consumer memory")
	}
}

func TestReferenceMalformedPresentationStateClosesSilently(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		fields     map[string]any
	}{
		{"zero-track", "metadata", map[string]any{"timestamp": 0, "track": 0}},
		{"negative-track", "metadata", map[string]any{"timestamp": 0, "track": -1}},
		{"missing-timestamp", "metadata", map[string]any{"title": "missing time"}},
		{"null-title", "metadata", map[string]any{"timestamp": 0, "title": nil}},
		{"incomplete-progress", "metadata", map[string]any{"timestamp": 0, "progress": map[string]any{"track_progress": 0, "track_duration": 0}}},
		{"negative-duration", "metadata", map[string]any{"timestamp": 0, "progress": map[string]any{"track_progress": 0, "track_duration": -1, "playback_speed": 0}}},
		{"negative-speed", "metadata", map[string]any{"timestamp": 0, "progress": map[string]any{"track_progress": 0, "track_duration": 0, "playback_speed": -1}}},
		{"null-primary", "color", map[string]any{"timestamp": 0, "primary": nil}},
		{"empty-primary", "color", map[string]any{"timestamp": 0, "primary": []int{}}},
		{"short-background", "color", map[string]any{"timestamp": 0, "background_dark": []int{0, 0}}},
		{"negative-accent", "color", map[string]any{"timestamp": 0, "accent": []int{-1, 0, 0}}},
		{"high-on-light", "color", map[string]any{"timestamp": 0, "on_light": []int{0, 0, 256}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, ref, store, connection := referencePresentationClient(t)
			if err := client.SetAvailable(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { return ref.control(t, map[string]any{"command": "status"})["available"] == false })
			before := store.Saves.Load()
			sendPairingFixtureMessage(t, ref, "server/state", map[string]any{tc.role: tc.fields})
			requireSilentProtocolClose(t, connection, ref)
			if store.Saves.Load() != before {
				t.Fatal("malformed presentation changed persistent settings")
			}
		})
	}
}
