//go:build e2e

package sendspin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
)

type observedEvent struct {
	Event
	At time.Time
}
type mediaEvents struct {
	mu     sync.Mutex
	events []observedEvent
}

func observeMedia(t *testing.T, client *Client) *mediaEvents {
	t.Helper()
	log := &mediaEvents{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-client.Events():
				if event.Artwork != nil || event.Visualization != nil {
					log.mu.Lock()
					log.events = append(log.events, observedEvent{Event: event, At: time.Now()})
					log.mu.Unlock()
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return log
}
func (log *mediaEvents) snapshot() []observedEvent {
	log.mu.Lock()
	defer log.mu.Unlock()
	return slices.Clone(log.events)
}
func (log *mediaEvents) artwork(channel int, timestamp int64) (Artwork, bool) {
	for _, event := range log.snapshot() {
		if event.Artwork != nil && event.Artwork.Channel == channel && event.Artwork.Timestamp == timestamp {
			return *event.Artwork, true
		}
	}
	return Artwork{}, false
}

func referenceMediaClient(t *testing.T, config Config) (*Client, referenceServer, *mediaEvents) {
	t.Helper()
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{101}
	ref := startReference(t, cred, psk, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &MemoryStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	config.Name = "Media roles E2E"
	config.Store = store
	config.Output = clockOutput{}
	if !slices.Contains(config.Roles, Player) {
		config.Roles = append([]Role{Player}, config.Roles...)
	}
	config.Formats = []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}}
	client, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	events := observeMedia(t, client)
	connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	return client, ref, events
}
func mediaCommand(t *testing.T, client *Client, ref referenceServer, command map[string]any) map[string]any {
	t.Helper()
	result := ref.control(t, command)
	if marker, ok := result["marker"].(string); ok {
		eventually(t, func() bool { return client.Snapshot().Group.Name == marker })
	}
	return result
}
func waitReferenceTimestamp(t *testing.T, ref referenceServer, timestamp int64) {
	t.Helper()
	eventually(t, func() bool {
		return int64(ref.control(t, map[string]any{"command": "clock"})["now"].(float64)) >= timestamp
	})
}

func TestE2EReferenceScheduledMetadataAndColor(t *testing.T) {
	client, ref, _ := referenceMediaClient(t, Config{Roles: []Role{MetadataRole, ColorRole}})
	mediaCommand(t, client, ref, map[string]any{"command": "metadata", "fields": map[string]any{"title": "current", "artist": "artist", "album": "album", "year": 2026, "track": 2, "track_progress": 100, "track_duration": 9000, "playback_speed": 1000}})
	mediaCommand(t, client, ref, map[string]any{"command": "color", "fields": map[string]any{"primary": []int{11, 22, 33}, "accent": []int{44, 55, 66}}})
	eventually(t, func() bool {
		s := client.Snapshot()
		return s.Metadata != nil && s.Metadata.Title == "current" && s.Colors != nil && slices.Equal(s.Colors.Primary, []int{11, 22, 33})
	})
	initial := client.Snapshot()
	if initial.Metadata.Progress == nil || initial.Metadata.Artist != "artist" || initial.Metadata.Year != 2026 {
		t.Fatal("initial full metadata did not arrive", initial.Metadata)
	}
	if position, ok := client.CurrentPosition(); !ok || position < 100 || position >= 9000 {
		t.Fatal("live track position did not use server clock mapping", position, ok)
	}
	first := mediaCommand(t, client, ref, map[string]any{"command": "metadata", "delay_ms": 1200, "fields": map[string]any{"title": "superseded"}})
	color := mediaCommand(t, client, ref, map[string]any{"command": "color", "delay_ms": 1200, "fields": map[string]any{"primary": []int{77, 88, 99}}})
	replacement := mediaCommand(t, client, ref, map[string]any{"command": "metadata", "delay_ms": 1900, "fields": map[string]any{"title": "replacement"}})
	if s := client.Snapshot(); s.Metadata.Title != "current" || !slices.Equal(s.Colors.Primary, []int{11, 22, 33}) {
		t.Fatal("scheduled state applied before its timestamp")
	}
	waitReferenceTimestamp(t, ref, int64(first["timestamp"].(float64))+100000)
	if client.Snapshot().Metadata.Title != "current" {
		t.Fatal("superseded metadata was still applied")
	}
	eventually(t, func() bool { return slices.Equal(client.Snapshot().Colors.Primary, []int{77, 88, 99}) })
	if client.Snapshot().Colors.Timestamp != int64(color["timestamp"].(float64)) {
		t.Fatal("color timestamp was not preserved")
	}
	eventually(t, func() bool { return client.Snapshot().Metadata.Title == "replacement" })
	s := client.Snapshot()
	if s.Metadata.Timestamp != int64(replacement["timestamp"].(float64)) || s.Metadata.Progress != nil || s.Metadata.Artist != "" || s.Metadata.Album != "" || s.Metadata.Year != 0 || s.Colors.Accent != nil {
		t.Fatal("full-state replacement retained omitted fields", s)
	}
	if _, ok := client.CurrentPosition(); ok {
		t.Fatal("omitted progress retained a live position")
	}
	mediaCommand(t, client, ref, map[string]any{"command": "metadata", "delay_ms": 800, "fields": map[string]any{"title": "canceled"}})
	mediaCommand(t, client, ref, map[string]any{"command": "metadata_clear"})
	mediaCommand(t, client, ref, map[string]any{"command": "color_clear"})
	waitReferenceTimestamp(t, ref, int64(ref.control(t, map[string]any{"command": "clock"})["now"].(float64))+1000000)
	s = client.Snapshot()
	if s.Metadata == nil || s.Metadata.Title != "" || s.Metadata.Progress != nil || s.Colors == nil || s.Colors.Primary != nil {
		t.Fatal("clear failed to cancel pending state", s)
	}
}

func TestE2EReferenceRoleRemovalDropsScheduledState(t *testing.T) {
	client, ref, _ := referenceMediaClient(t, Config{Roles: []Role{MetadataRole, ColorRole}})
	mediaCommand(t, client, ref, map[string]any{"command": "metadata", "fields": map[string]any{"title": "current"}})
	mediaCommand(t, client, ref, map[string]any{"command": "color", "fields": map[string]any{"primary": []int{1, 2, 3}}})
	metadata := mediaCommand(t, client, ref, map[string]any{"command": "metadata", "delay_ms": 800, "fields": map[string]any{"title": "after-removal"}})
	color := mediaCommand(t, client, ref, map[string]any{"command": "color", "delay_ms": 800, "fields": map[string]any{"primary": []int{4, 5, 6}}})
	mediaCommand(t, client, ref, map[string]any{"command": "roles", "roles": []string{"player@v1"}})
	if s := client.Snapshot(); s.Metadata != nil || s.Colors != nil || !slices.Equal(s.ActiveRoles, []Role{Player}) {
		t.Fatal("removed roles retained state", s)
	}
	due := max(int64(metadata["timestamp"].(float64)), int64(color["timestamp"].(float64)))
	waitReferenceTimestamp(t, ref, due+150000)
	if s := client.Snapshot(); s.Metadata != nil || s.Colors != nil {
		t.Fatal("removed role's pending update reappeared")
	}
	mediaCommand(t, client, ref, map[string]any{"command": "roles", "roles": []string{"player@v1", "metadata@v1", "color@v1"}})
	eventually(t, func() bool {
		s := client.Snapshot()
		return s.Metadata != nil && s.Metadata.Title == "after-removal" && s.Colors != nil && slices.Equal(s.Colors.Primary, []int{4, 5, 6})
	})
}

func assertReferenceArtwork(t *testing.T, log *mediaEvents, result map[string]any, dimensions map[int]image.Point) {
	t.Helper()
	for key, raw := range result["images"].(map[string]any) {
		channel, err := strconv.Atoi(key)
		if err != nil {
			t.Fatal(err)
		}
		record := raw.(map[string]any)
		timestamp := int64(record["timestamp"].(float64))
		var picture Artwork
		eventually(t, func() bool { var ok bool; picture, ok = log.artwork(channel, timestamp); return ok })
		hash := sha256.Sum256(picture.Data)
		if hex.EncodeToString(hash[:]) != record["sha256"] || len(picture.Data) != int(record["bytes"].(float64)) {
			t.Fatalf("encoded artwork mismatch on channel %d", channel)
		}
		if len(picture.Data) == 0 {
			continue
		}
		decoded, _, err := image.Decode(bytes.NewReader(picture.Data))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := decoded.Bounds().Size(), dimensions[channel]; got != want {
			t.Fatalf("channel %d size %v, want %v", channel, got, want)
		}
		if channel == 0 {
			r, g, b, _ := decoded.At(0, 0).RGBA()
			if r != 0 || g != 0 || b != 0 {
				t.Fatal("aspect-ratio padding was not preserved")
			}
		}
		if int(record["max_frame_bytes"].(float64)) > 65519 {
			t.Fatal("artwork exceeded single-Noise-message framing cap")
		}
	}
}

func TestE2EReferenceMultipartArtworkAndChannels(t *testing.T) {
	client, ref, events := referenceMediaClient(t, Config{Roles: []Role{ArtworkRole}, Artwork: []ArtworkChannel{
		{Source: "album", Format: "png", Width: 512, Height: 512}, {Source: "artist", Format: "jpeg", Width: 160, Height: 90}, {Source: "none"}, {Source: "album", Format: "jpeg", Width: 64, Height: 64},
	}})
	dimensions := map[int]image.Point{0: {X: 512, Y: 512}, 1: {X: 160, Y: 90}, 3: {X: 64, Y: 64}}
	album := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "source": "album", "large": true})
	assertReferenceArtwork(t, events, album, dimensions)
	if current, ok := client.CurrentArtwork(0); !ok || current.Timestamp != int64(album["timestamp"].(float64)) {
		t.Fatal("current artwork is not observable")
	}
	if record := album["images"].(map[string]any)["0"].(map[string]any); record["bytes"].(float64) <= 65519 || record["parts"].(float64) < 2 {
		t.Fatal("fixture did not exercise multipart artwork")
	}
	artist := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "source": "artist", "seed": 2})
	assertReferenceArtwork(t, events, artist, dimensions)
	// Mutating an application-owned event must not corrupt the SDK's read model.
	fromEvent, ok := events.artwork(0, int64(album["timestamp"].(float64)))
	if !ok {
		t.Fatal("missing album event")
	}
	fromEvent.Data[0] ^= 0xff
	current, ok := client.CurrentArtwork(0)
	if !ok {
		t.Fatal("missing cached album")
	}
	hash := sha256.Sum256(current.Data)
	if hex.EncodeToString(hash[:]) != album["images"].(map[string]any)["0"].(map[string]any)["sha256"] {
		t.Fatal("event mutation altered current artwork")
	}
	for _, event := range events.snapshot() {
		if event.Artwork != nil && event.Artwork.Channel == 2 && len(event.Artwork.Data) > 0 {
			t.Fatal("disabled artwork channel received image data")
		}
	}
}

func TestE2EReferenceScheduledArtworkCancelAndRoleRemoval(t *testing.T) {
	client, ref, events := referenceMediaClient(t, Config{Roles: []Role{ArtworkRole}, Artwork: []ArtworkChannel{{Source: "album", Format: "png", Width: 64, Height: 64}}})
	dimensions := map[int]image.Point{0: {X: 64, Y: 64}}
	current := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 3})
	assertReferenceArtwork(t, events, current, dimensions)
	future := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 4, "delay_ms": 1000})
	futureTime := int64(future["timestamp"].(float64))
	if _, ok := events.artwork(0, futureTime); ok {
		t.Fatal("future artwork displayed before timestamp")
	}
	mediaCommand(t, client, ref, map[string]any{"command": "artwork_cancel"})
	waitReferenceTimestamp(t, ref, futureTime+150000)
	if _, ok := events.artwork(0, futureTime); ok {
		t.Fatal("canceled artwork was rendered")
	}
	if currentImage, ok := client.CurrentArtwork(0); !ok || currentImage.Timestamp != int64(current["timestamp"].(float64)) {
		t.Fatal("cancellation cleared the current image")
	}
	future = mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 5, "delay_ms": 800})
	futureTime = int64(future["timestamp"].(float64))
	mediaCommand(t, client, ref, map[string]any{"command": "roles", "roles": []string{"player@v1"}})
	if _, ok := client.CurrentArtwork(0); ok {
		t.Fatal("role removal retained exposed artwork")
	}
	if slices.Contains(client.Snapshot().ActiveStreams, "artwork") {
		t.Fatal("role removal retained active artwork stream")
	}
	eventually(t, func() bool {
		for _, e := range events.snapshot() {
			if e.Artwork != nil && e.Artwork.Channel == 0 && len(e.Artwork.Data) == 0 && e.Artwork.Timestamp == 0 {
				return true
			}
		}
		return false
	})
	waitReferenceTimestamp(t, ref, futureTime+150000)
	if _, ok := events.artwork(0, futureTime); ok {
		t.Fatal("role removal retained scheduled artwork")
	}
}

func visualizationBytes(v Visualization) []byte {
	ids := map[string]byte{"loudness": 16, "beat": 17, "f_peak": 18, "spectrum": 19, "peak": 20}
	result := binary.BigEndian.AppendUint64([]byte{ids[v.Type]}, uint64(v.Timestamp))
	switch v.Type {
	case "beat":
		if v.Downbeat {
			return append(result, 1)
		}
		return append(result, 0)
	case "peak":
		return append(result, v.Strength)
	default:
		for _, value := range v.Values {
			result = binary.BigEndian.AppendUint16(result, value)
		}
		return result
	}
}

func TestE2EReferenceVisualizerVectorsAndBeat(t *testing.T) {
	for _, beatOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("beat=%t", beatOnly), func(t *testing.T) {
			config := VisualizerConfig{Types: []string{"loudness", "f_peak", "spectrum", "peak"}, RateMax: 30, Spectrum: &Spectrum{Bins: 4, Scale: "lin", MinFrequency: 20, MaxFrequency: 20000}}
			if beatOnly {
				config = VisualizerConfig{Types: []string{"beat"}, RateMax: 30}
			}
			client, ref, events := referenceMediaClient(t, Config{Roles: []Role{VisualizerRole}, Visualizer: config})
			vectors := mediaCommand(t, client, ref, map[string]any{"command": "visualizer", "delay_ms": 1000, "beat": beatOnly})
			frames := vectors["frames"].([]any)
			want := 4
			if beatOnly {
				want = 1
			}
			if len(frames) != want {
				t.Fatalf("reference produced %d frames, want %d", len(frames), want)
			}
			timestamp := int64(vectors["timestamp"].(float64))
			for _, event := range events.snapshot() {
				if event.Visualization != nil && event.Visualization.Timestamp == timestamp {
					t.Fatal("visualizer frame rendered early")
				}
			}
			expected := map[string]bool{}
			for _, raw := range frames {
				expected[raw.(map[string]any)["hex"].(string)] = true
			}
			eventually(t, func() bool {
				seen := map[string]bool{}
				for _, e := range events.snapshot() {
					if e.Visualization != nil && e.Visualization.Timestamp == timestamp {
						seen[hex.EncodeToString(visualizationBytes(*e.Visualization))] = true
					}
				}
				if len(seen) != len(expected) {
					return false
				}
				for key := range expected {
					if !seen[key] {
						return false
					}
				}
				return true
			})
		})
	}
}

func TestE2EReferenceVisualizerClearAndEndDropFutureFrames(t *testing.T) {
	for _, boundary := range []string{"visualizer_clear", "visualizer_end", "roles"} {
		t.Run(boundary, func(t *testing.T) {
			client, ref, events := referenceMediaClient(t, Config{Roles: []Role{VisualizerRole}, Visualizer: VisualizerConfig{Types: []string{"loudness", "f_peak", "spectrum", "peak"}, RateMax: 30, Spectrum: &Spectrum{Bins: 4, Scale: "lin", MinFrequency: 20, MaxFrequency: 20000}}})
			vectors := mediaCommand(t, client, ref, map[string]any{"command": "visualizer", "delay_ms": 800})
			timestamp := int64(vectors["timestamp"].(float64))
			command := map[string]any{"command": boundary}
			if boundary == "roles" {
				command["roles"] = []string{"player@v1"}
			}
			mediaCommand(t, client, ref, command)
			active := slices.Contains(client.Snapshot().ActiveStreams, "visualizer")
			if active != (boundary == "visualizer_clear") {
				t.Fatal("stream boundary was not reflected in Snapshot")
			}
			waitReferenceTimestamp(t, ref, timestamp+150000)
			for _, e := range events.snapshot() {
				if e.Visualization != nil && e.Visualization.Timestamp == timestamp {
					t.Fatal("stream boundary retained a scheduled visualizer frame")
				}
			}
			if !client.Snapshot().Available {
				t.Fatal("stream boundary disconnected client")
			}
		})
	}
}

func TestE2EReferenceScheduledArtworkReplacementClearAndResume(t *testing.T) {
	ctx := context.Background()
	client, ref, events := referenceMediaClient(t, Config{Roles: []Role{ArtworkRole}, Artwork: []ArtworkChannel{{Source: "album", Format: "png", Width: 64, Height: 64}}})
	dimensions := map[int]image.Point{0: {X: 64, Y: 64}}
	current := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 6})
	assertReferenceArtwork(t, events, current, dimensions)
	first := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 7, "delay_ms": 1000})
	replacement := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 8, "delay_ms": 1700})
	if picture, ok := client.CurrentArtwork(0); !ok || picture.Timestamp != int64(current["timestamp"].(float64)) {
		t.Fatal("pending image replaced current image early")
	}
	waitReferenceTimestamp(t, ref, int64(first["timestamp"].(float64))+100000)
	if _, ok := events.artwork(0, int64(first["timestamp"].(float64))); ok {
		t.Fatal("superseded image was rendered")
	}
	assertReferenceArtwork(t, events, replacement, dimensions)
	clear := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "clear": true, "delay_ms": 700})
	if _, ok := client.CurrentArtwork(0); !ok {
		t.Fatal("scheduled empty image cleared current artwork early")
	}
	assertReferenceArtwork(t, events, clear, dimensions)
	if _, ok := client.CurrentArtwork(0); ok {
		t.Fatal("scheduled empty image did not clear the read model")
	}
	if !slices.Contains(client.Snapshot().ActiveStreams, "artwork") {
		t.Fatal("empty image incorrectly ended the stream")
	}
	latest := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 9})
	assertReferenceArtwork(t, events, latest, dimensions)
	if err := client.SetAvailable(ctx, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !slices.Contains(client.Snapshot().ActiveStreams, "artwork") })
	if _, ok := client.CurrentArtwork(0); ok {
		t.Fatal("stream end retained exposed image")
	}
	if !slices.Contains(client.Snapshot().ActiveRoles, ArtworkRole) {
		t.Fatal("unavailability incorrectly removed the artwork role")
	}
	if err := client.SetAvailable(ctx, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return slices.Contains(client.Snapshot().ActiveStreams, "artwork") })
	resumed := mediaCommand(t, client, ref, map[string]any{"command": "artwork", "seed": 10})
	assertReferenceArtwork(t, events, resumed, dimensions)
}
