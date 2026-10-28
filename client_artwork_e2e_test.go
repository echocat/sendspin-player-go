//go:build e2e

package sendspin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strconv"
	"testing"
)

func referenceArtworkProtocolClient(t *testing.T, channels ...ArtworkChannel) (*Client, referenceServer, *outboundTestConnection) {
	t.Helper()
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{123}
	ref := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if len(channels) == 0 {
		channels = []ArtworkChannel{{Source: "album", Format: "png", Width: 32, Height: 32}, {Source: "artist", Format: "png", Width: 32, Height: 32}, {Source: "none"}}
	}
	client, err := New(ctx, Config{Name: "Artwork protocol", Store: store, Roles: []Role{ArtworkRole}, Artwork: channels})
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool {
		return client.Snapshot().Available && slices.Contains(client.Snapshot().ActiveStreams, "artwork")
	})
	mediaCommand(t, client, ref, map[string]any{"command": "barrier"})
	return client, ref, connection
}

func TestReferenceInFlightArtworkCancellationReplacementAndRemoval(t *testing.T) {
	for _, mode := range []string{"cancel", "replace", "remove"} {
		t.Run(mode, func(t *testing.T) {
			client, ref, events := referenceMediaClient(t, Config{Roles: []Role{ArtworkRole}, Artwork: []ArtworkChannel{{Source: "album", Format: "png", Width: 512, Height: 512}}})
			current := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 30})
			before, ok := client.CurrentArtwork(0)
			if !ok {
				t.Fatal("initial artwork missing")
			}
			started := ref.control(t, map[string]any{"command": "artwork_inflight_start", "seed": 31})
			marker := started["gate_marker"].(string)
			eventually(t, func() bool { return client.Snapshot().Group.Name == marker })
			// The peer's same-direction marker followed a real first image part,
			// not merely an enqueued part. No incomplete/pending image is exposed.
			image, ok := client.CurrentArtwork(0)
			if !ok || image.Timestamp != before.Timestamp || !bytes.Equal(image.Data, before.Data) {
				t.Fatal("partial transfer replaced current image")
			}
			firstTimestamp := int64(started["timestamp"].(float64))
			if _, found := events.artwork(0, firstTimestamp); found {
				t.Fatal("partial future transfer emitted an image event")
			}
			finished := mediaCommand(t, client, ref, map[string]any{"command": "artwork_inflight_finish", "mode": mode, "seed": 32})
			cancelIndex, endIndex, newAnnounce := -1, -1, -1
			firstBytes := 0
			for i, raw := range finished["frames"].([]any) {
				frame := raw.(map[string]any)
				if frame["type"] == "stream/end" {
					endIndex = i
					continue
				}
				if flags, ok := frame["flags"].(float64); ok {
					if frame["bytes"].(float64) > 65519 {
						t.Fatal("native artwork frame exceeded wire cap")
					}
					if flags == 1 {
						cancelIndex = i
					}
					if flags == 0 && frame["timestamp"] == float64(firstTimestamp) {
						firstBytes += int(frame["bytes"].(float64)) - 2
					}
					if flags == 2 && frame["timestamp"] != float64(firstTimestamp) {
						newAnnounce = i
					}
				}
			}
			if firstBytes <= 0 || firstBytes >= int(started["bytes"].(float64)) || cancelIndex < 0 {
				t.Fatal("probe did not exercise native in-flight cancel", finished)
			}
			if mode == "replace" {
				if newAnnounce <= cancelIndex {
					t.Fatal("replacement announce did not follow native cancel")
				}
				image, ok = client.CurrentArtwork(0)
				if !ok || image.Timestamp != int64(finished["timestamp"].(float64)) {
					t.Fatal("late replacement was dropped or remained pending")
				}
				sum := sha256.Sum256(image.Data)
				if hex.EncodeToString(sum[:]) != finished["sha256"] || len(image.Data) != int(finished["bytes"].(float64)) {
					t.Fatal("replacement image differs from independent encoded source")
				}
			} else if mode == "cancel" {
				image, ok = client.CurrentArtwork(0)
				if !ok || !bytes.Equal(image.Data, before.Data) || image.Timestamp != int64(current["timestamp"].(float64)) {
					t.Fatal("cancel cleared the current image")
				}
			} else {
				if endIndex <= cancelIndex {
					t.Fatal("native stream/end did not follow cancel")
				}
				if _, ok = client.CurrentArtwork(0); ok || slices.Contains(client.Snapshot().ActiveStreams, "artwork") || slices.Contains(client.Snapshot().ActiveRoles, ArtworkRole) {
					t.Fatal("role removal retained image/stream/role")
				}
			}
			if _, found := events.artwork(0, firstTimestamp); found {
				t.Fatal("canceled partial image became visible")
			}
		})
	}
}

func TestReferenceMalformedArtworkWireClosesSilently(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames [][]byte
		end    bool
	}{
		{name: "short", frames: [][]byte{{8}}},
		{name: "cancel-data", frames: [][]byte{{8, 1, 0}}},
		{name: "both-flags", frames: [][]byte{{8, 3}}},
		{name: "reserved-flag", frames: [][]byte{{8, 4}}},
		{name: "short-announce", frames: [][]byte{artworkAnnounce(0, 0, 1)[:13]}},
		{name: "long-announce", frames: [][]byte{append(artworkAnnounce(0, 0, 1), 0)}},
		{name: "orphan-part", frames: [][]byte{{8, 0, 1}}},
		{name: "disabled-channel", frames: [][]byte{{10, 1}}},
		{name: "overlap-other-channel", frames: [][]byte{artworkAnnounce(0, 0, 4), artworkAnnounce(1, 0, 4)}},
		{name: "part-other-channel", frames: [][]byte{artworkAnnounce(0, 0, 4), {9, 0, 1}}},
		{name: "overrun", frames: [][]byte{artworkAnnounce(0, 0, 1), {8, 0, 1, 2}}},
		{name: "oversized-part", frames: [][]byte{artworkAnnounce(0, 0, 65518), append([]byte{8, 0}, make([]byte, 65518)...)}},
		{name: "uncancelled-end", frames: [][]byte{artworkAnnounce(0, 0, 4), {8, 0, 1}}, end: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A no-audio artwork client avoids conflating playback deadlines with
			// the role protocol errors under test.
			_, ref, connection := referenceArtworkProtocolClient(t)
			for _, frame := range tc.frames {
				ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString(frame)})
			}
			if tc.end {
				sendPairingFixtureMessage(t, ref, "stream/end", map[string]any{"roles": []string{"artwork"}})
			}
			requireSilentProtocolClose(t, connection, ref)
		})
	}
}

func TestReferenceAllDisabledArtworkStreamStaysActiveWithoutImages(t *testing.T) {
	client, ref, connection := referenceArtworkProtocolClient(t, ArtworkChannel{Source: "none"})
	if _, ok := client.CurrentArtwork(0); ok || !slices.Contains(client.Snapshot().ActiveStreams, "artwork") {
		t.Fatal("empty server channel array closed stream or invented an image")
	}
	ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString([]byte{8, 1})})
	requireSilentProtocolClose(t, connection, ref)
}

func TestReferenceArtworkConfigurationMustMatchClientDeclaration(t *testing.T) {
	for i, channels := range []any{
		nil, []any{}, []any{nil},
		[]any{map[string]any{"source": "album", "format": "jpeg", "width": 32, "height": 32}},
		[]any{map[string]any{"source": "album", "format": "png", "width": 33, "height": 32}},
		[]any{map[string]any{"format": "png", "width": 32, "height": 32}},
		[]any{map[string]any{"source": "none"}, map[string]any{"source": "none"}, map[string]any{"source": "none"}, map[string]any{"source": "none"}, map[string]any{"source": "none"}},
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			_, ref, connection := referenceArtworkProtocolClient(t)
			sendPairingFixtureMessage(t, ref, "stream/start", map[string]any{"server_transmitted": 0, "artwork": map[string]any{"channels": channels}})
			requireSilentProtocolClose(t, connection, ref)
		})
	}
}
