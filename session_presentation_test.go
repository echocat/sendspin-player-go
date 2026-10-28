package sendspin

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func TestMetadataTrackNumberIsOneIndexedWhenPresent(t *testing.T) {
	for _, track := range []string{"0", "-1"} {
		s := mediaSession(t)
		if err := s.serverState([]byte(`{"metadata":{"timestamp":0,"track":` + track + `}}`)); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("invalid explicit track number accepted", track, err)
		}
	}
}

func TestMetadataProgressWorkingDomainPreservesWideIntegers(t *testing.T) {
	// WA02 chooses nonnegative progress fields, not sendspin-cpp's uint32 API
	// limit. This tests our policy; it does not settle the RC1 numeric-domain text.
	for _, value := range []int64{0, 1, math.MaxUint32, math.MaxUint32 + 1, 9007199254740993, math.MaxInt64} {
		want := &Progress{PositionMS: value, DurationMS: value, Speed: value}
		raw, err := json.Marshal(Metadata{Timestamp: -17, Progress: want})
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeMetadataState(raw)
		if err != nil || got.Timestamp != -17 || !reflect.DeepEqual(got.Progress, want) {
			t.Fatal("working-domain integer was truncated, clamped or rejected", value, got, err)
		}
	}
	metadata, err := decodeMetadataState([]byte(`{"timestamp":0,"progress":{"track_progress":4294967296,"track_duration":0,"playback_speed":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if position, known := metadata.Position(math.MaxInt64); !known || position != 4294967296 {
		t.Fatal("zero duration/speed lost exact unlimited paused progress", position, known)
	}
}

func TestMetadataProgressWorkingPolicyRejectsWithoutReplacingState(t *testing.T) {
	// Unlike the C++ parser's field-ignore behavior, this SDK fails the known
	// object. No replacement or cancellation may leak from that rejected update.
	for _, field := range []string{"track_progress", "track_duration", "playback_speed"} {
		for _, value := range []json.RawMessage{json.RawMessage(`-1`), json.RawMessage(`-9223372036854775808`), json.RawMessage(`9223372036854775808`), json.RawMessage(`1.5`), json.RawMessage(`"1"`), json.RawMessage(`null`)} {
			t.Run(field+"/"+string(value), func(t *testing.T) {
				s := mediaSession(t)
				current := &Metadata{Title: "current", Progress: &Progress{PositionMS: 19, DurationMS: 100, Speed: 1000}}
				pending := &Metadata{Timestamp: s.now() + 5_000_000, Title: "pending"}
				s.state.Metadata, s.pendingMetadata = current, pending
				fields := map[string]json.RawMessage{"track_progress": json.RawMessage(`1`), "track_duration": json.RawMessage(`100`), "playback_speed": json.RawMessage(`1000`)}
				fields[field] = value
				raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"timestamp": 0, "progress": fields}})
				if err != nil {
					t.Fatal(err)
				}
				if err = s.serverState(raw); !errors.Is(err, protocol.ErrProtocol) || s.state.Metadata != current || s.pendingMetadata != pending {
					t.Fatal("rejected field changed applied/pending metadata", err)
				}
			})
		}
	}
}

func exactMetadataPosition(m Metadata, now int64) int64 {
	numerator := new(big.Int).Sub(big.NewInt(now), big.NewInt(m.Timestamp))
	numerator.Mul(numerator, big.NewInt(m.Progress.Speed))
	base := new(big.Int).Mul(big.NewInt(m.Progress.PositionMS), big.NewInt(1_000_000))
	numerator.Add(numerator, base)
	if numerator.Sign() <= 0 {
		return 0
	}
	numerator.Quo(numerator, big.NewInt(1_000_000))
	limit := int64(math.MaxInt64)
	if m.Progress.DurationMS > 0 {
		limit = m.Progress.DurationMS
	}
	if numerator.Cmp(big.NewInt(limit)) >= 0 {
		return limit
	}
	return numerator.Int64()
}

func TestMetadataPositionMatchesExactArithmetic(t *testing.T) {
	values := []int64{math.MinInt64, math.MinInt64 + 1, -1, 0, 1, math.MaxInt64 - 1, math.MaxInt64}
	for _, timestamp := range values {
		for _, now := range values {
			for _, position := range values {
				for _, speed := range values {
					for _, duration := range values {
						m := Metadata{Timestamp: timestamp, Progress: &Progress{PositionMS: position, Speed: speed, DurationMS: duration}}
						want := exactMetadataPosition(m, now)
						if got, ok := m.Position(now); !ok || got != want {
							t.Fatalf("position=%d known=%t want=%d for timestamp=%d now=%d progress=%+v", got, ok, want, timestamp, now, *m.Progress)
						}
					}
				}
			}
		}
	}
	if _, ok := (Metadata{}).Position(0); ok {
		t.Fatal("absent progress invented a position")
	}
	m := Metadata{Progress: &Progress{PositionMS: 9007199254740993, Speed: 0}}
	var got int64
	var known bool
	if count := testing.AllocsPerRun(100, func() { got, known = m.Position(math.MaxInt64) }); count != 0 || !known || got != 9007199254740993 {
		t.Fatal("exact position allocates or changed paused progress", count, got, known)
	}
}

func FuzzMetadataPosition(f *testing.F) {
	f.Add(int64(0), int64(0), int64(9007199254740993), int64(0), int64(0))
	f.Add(int64(math.MinInt64), int64(math.MaxInt64), int64(100), int64(9000), int64(math.MaxInt64))
	f.Add(int64(1<<60), int64((1<<60)+999), int64(0), int64(0), int64(1000))
	f.Fuzz(func(t *testing.T, timestamp, now, position, duration, speed int64) {
		m := Metadata{Timestamp: timestamp, Progress: &Progress{PositionMS: position, DurationMS: duration, Speed: speed}}
		if got, ok := m.Position(now); !ok || got != exactMetadataPosition(m, now) {
			t.Fatal("position differs from arbitrary-precision formula", got, ok, m, now)
		}
	})
}

func TestPresentationFieldValidationAndReplacement(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"timestamp":null}`, `{"timestamp":1.5}`, `{"timestamp":0,"title":null}`, `{"timestamp":0,"artist":4}`,
		`{"timestamp":0,"album_artist":[]}`, `{"timestamp":0,"album":false}`, `{"timestamp":0,"artwork_url":null}`, `{"timestamp":0,"year":"2026"}`,
		`{"timestamp":0,"track":null}`, `{"timestamp":0,"track":0}`, `{"timestamp":0,"progress":null}`, `{"timestamp":0,"progress":{}}`,
		`{"timestamp":0,"progress":{"track_progress":0,"track_duration":0}}`,
		`{"timestamp":0,"progress":{"track_progress":null,"track_duration":0,"playback_speed":0}}`,
		`{"timestamp":0,"progress":{"track_progress":-1,"track_duration":0,"playback_speed":0}}`,
		`{"timestamp":0,"progress":{"track_progress":0,"track_duration":-1,"playback_speed":0}}`,
		`{"timestamp":0,"progress":{"track_progress":0,"track_duration":0,"playback_speed":-1}}`,
	} {
		if _, err := decodeMetadataState([]byte(raw)); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("invalid metadata accepted", raw, err)
		}
	}
	for _, name := range []string{"background_dark", "background_light", "primary", "accent", "on_dark", "on_light"} {
		for _, value := range []any{nil, []int{}, []int{0, 1}, []int{0, 1, 2, 3}, []int{-1, 0, 0}, []int{0, 0, 256}, []any{0, nil, 0}, []any{0, 1.5, 0}, "unused"} {
			raw, _ := json.Marshal(map[string]any{"timestamp": 0, name: value})
			if _, err := decodeColorState(raw); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("invalid RGB field accepted", name, value, err)
			}
		}
	}
	s := mediaSession(t)
	if err := s.serverState([]byte(`{"metadata":{"timestamp":0,"title":"current","artist":"artist","track":1,"progress":{"track_progress":0,"track_duration":0,"playback_speed":0},"TRACK":null,"future":false},"color":{"timestamp":0,"primary":[0,255,0],"accent":[255,0,255],"PRIMARY":null,"future":4}}`)); err != nil {
		t.Fatal(err)
	}
	if s.state.Metadata.Track != 1 || s.state.Metadata.Progress == nil || s.state.Colors.Primary[1] != 255 {
		t.Fatal("exact-case fields changed")
	}
	m, c := s.state.Metadata, s.state.Colors
	if err := s.serverState([]byte(`{"future":null}`)); err != nil || s.state.Metadata != m || s.state.Colors != c {
		t.Fatal("omitted objects changed applied state", err)
	}
	if err := s.serverState([]byte(`{"metadata":{"timestamp":0},"color":{"timestamp":0}}`)); err != nil {
		t.Fatal(err)
	}
	if s.state.Metadata.Progress != nil || s.state.Metadata.Track != 0 || s.state.Metadata.Title != "" || s.state.Colors.Primary != nil || s.state.Colors.Accent != nil {
		t.Fatal("timestamp-only replacement failed to clear omitted fields")
	}
	s.client.active = s
	s.state.Metadata = m
	s.state.Colors = c
	s.publish()
	copy := s.client.Snapshot()
	copy.Metadata.Progress.PositionMS = 999
	copy.Colors.Primary[0] = 99
	if got := s.client.Snapshot(); !reflect.DeepEqual(got.Metadata, m) || !reflect.DeepEqual(got.Colors, c) {
		t.Fatal("consumer changed presentation state through snapshot")
	}
}

func TestPresentationOpaqueFieldsAndUnadjustedRGB(t *testing.T) {
	// Text/URL/year do not acquire invented scheme/calendar restrictions, and
	// receiver RGB parsing is not the server's WCAG palette-generation policy.
	url := "opaque:unfetched artwork hint with spaces"
	raw, err := json.Marshal(map[string]any{"timestamp": 0, "title": "曲", "artist": "", "album_artist": "artist", "album": "album", "artwork_url": url, "year": math.MaxInt, "track": 1})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := decodeMetadataState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Title != "曲" || metadata.ArtworkURL != url || metadata.Year != math.MaxInt || metadata.Track != 1 {
		t.Fatal("metadata parser repaired opaque fields")
	}
	palette, err := decodeColorState([]byte(`{"timestamp":0,"background_dark":[255,255,255],"background_light":[0,0,0],"on_dark":[0,0,0],"on_light":[255,255,255],"primary":[1,2,3],"accent":[4,5,6]}`))
	if err != nil {
		t.Fatal("receiver imposed server contrast-generation checks", err)
	}
	want := Colors{BackgroundDark: []int{255, 255, 255}, BackgroundLight: []int{0, 0, 0}, OnDark: []int{0, 0, 0}, OnLight: []int{255, 255, 255}, Primary: []int{1, 2, 3}, Accent: []int{4, 5, 6}}
	if !reflect.DeepEqual(palette, want) {
		t.Fatal("receiver recolored or discarded valid RGB values")
	}
}

func TestPresentationSchedulingUsesBestEstimateWithoutAvailabilityGate(t *testing.T) {
	s := mediaSession(t)
	s.state.Available = false
	if s.filter.Ready() {
		t.Fatal("test clock unexpectedly converged")
	}
	if err := s.serverState([]byte(`{"metadata":{"timestamp":0,"title":"current"},"color":{"timestamp":0,"primary":[1,2,3]}}`)); err != nil {
		t.Fatal(err)
	}
	future := s.now() + 5_000_000
	raw, _ := json.Marshal(map[string]any{"metadata": Metadata{Timestamp: future, Title: "superseded"}, "color": Colors{Timestamp: future, Primary: []int{4, 5, 6}}})
	if err := s.serverState(raw); err != nil {
		t.Fatal(err)
	}
	oldColor := s.pendingColors
	raw, _ = json.Marshal(map[string]any{"metadata": Metadata{Timestamp: future + 1_000_000, Title: "replacement"}})
	if err := s.serverState(raw); err != nil {
		t.Fatal(err)
	}
	if s.pendingMetadata == nil || s.pendingMetadata.Title != "replacement" || s.pendingColors != oldColor || s.state.Metadata.Title != "current" {
		t.Fatal("per-role pending state was merged or applied early")
	}
	if err := s.serverState([]byte(`{"future":null}`)); err != nil || s.pendingColors != oldColor {
		t.Fatal("omitted object canceled its pending update", err)
	}
	// One initial sample is enough to refine the best estimate. Presentation
	// scheduling must not wait for the audio readiness/convergence threshold.
	s.filter.Update(future+2_000_000, 1, s.now()+1)
	if s.filter.Ready() {
		t.Fatal("test prematurely satisfied the convergence gate")
	}
	if err := s.scheduled(); err != nil {
		t.Fatal(err)
	}
	if s.pendingMetadata != nil || s.pendingColors != nil || s.state.Metadata.Title != "replacement" || s.state.Colors.Primary[0] != 4 {
		t.Fatal("best-estimate due updates were withheld while unavailable/unconverged")
	}
	// Removal, unlike availability changes, discards current and pending data.
	s.pendingMetadata = &Metadata{Timestamp: future + int64(time.Hour/time.Microsecond)}
	s.pendingColors = &Colors{Timestamp: future + int64(time.Hour/time.Microsecond)}
	s.clearRole("metadata")
	s.clearRole("color")
	if s.state.Metadata != nil || s.state.Colors != nil || s.pendingMetadata != nil || s.pendingColors != nil {
		t.Fatal("role removal retained presentation state")
	}
}

func TestMetadataPositionDoesNotLoseIntegerPrecision(t *testing.T) {
	for _, tc := range []struct{ timestamp, now, position, speed, want int64 }{
		{position: 9007199254740993, want: 9007199254740993},
		{position: 9223372036854775806, want: 9223372036854775806},
		{timestamp: 1 << 60, now: (1 << 60) + 999, speed: 1000, want: 0},
		{timestamp: 1 << 60, now: (1 << 60) + 1, speed: 1000000, want: 1},
		{timestamp: 10, now: 9, position: 10, speed: 1, want: 9},
	} {
		metadata := Metadata{Timestamp: tc.timestamp, Progress: &Progress{PositionMS: tc.position, Speed: tc.speed}}
		if got, ok := metadata.Position(tc.now); !ok || got != tc.want {
			t.Fatalf("position %d known=%t, want exact %d for %+v", got, ok, tc.want, tc)
		}
	}
}
