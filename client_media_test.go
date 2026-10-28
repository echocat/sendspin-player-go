package sendspin

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"sync"
	"testing"
)

func TestCurrentArtworkIsIndependentOfEventsAndDefensive(t *testing.T) {
	s := mediaSession(t)
	s.client.active = s
	s.applyArtwork(Artwork{Channel: 0, Timestamp: 42, Data: []byte{1, 2, 3}})
	image, ok := s.client.CurrentArtwork(0)
	if !ok {
		t.Fatal("missing current image")
	}
	image.Data[0] = 99
	event := <-s.client.Events()
	event.Artwork.Data[1] = 88
	image, ok = s.client.CurrentArtwork(0)
	if !ok || image.Timestamp != 42 || !bytes.Equal(image.Data, []byte{1, 2, 3}) {
		t.Fatal("consumer mutated the read model", image)
	}
	for range cap(s.client.events) {
		s.client.emit(Event{Kind: "state_changed"})
	}
	s.applyArtwork(Artwork{Channel: 0, Timestamp: 43, Data: []byte{4, 5, 6}})
	image, ok = s.client.CurrentArtwork(0)
	if !ok || image.Timestamp != 43 {
		t.Fatal("full event buffer lost current artwork")
	}
	s.clearRole("artwork")
	if _, ok = s.client.CurrentArtwork(0); ok {
		t.Fatal("full event buffer prevented artwork clearing")
	}
	for _, channel := range []int{-1, 4} {
		if _, ok = s.client.CurrentArtwork(channel); ok {
			t.Fatal("accepted invalid channel")
		}
	}
	s.client.active = nil
	if _, ok = s.client.CurrentArtwork(0); ok {
		t.Fatal("disconnected device retained an exposed image")
	}
}

func TestActiveStreamsAreObservableAndDoNotAlias(t *testing.T) {
	s := mediaSession(t)
	s.client.active = s
	s.streams["visualizer"] = true
	s.streams["artwork"] = true
	s.publish()
	snapshot := s.client.Snapshot()
	if !slices.Equal(snapshot.ActiveStreams, []string{"artwork", "visualizer"}) {
		t.Fatal(snapshot.ActiveStreams)
	}
	snapshot.ActiveStreams[0] = "changed"
	if !slices.Equal(s.client.Snapshot().ActiveStreams, []string{"artwork", "visualizer"}) {
		t.Fatal("snapshot aliased active streams")
	}
	if err := s.stopStream("stream/end", []byte(`{"roles":["artwork"]}`)); err != nil {
		t.Fatal(err)
	}
	if got := s.client.Snapshot().ActiveStreams; !slices.Equal(got, []string{"visualizer"}) {
		t.Fatal("stream end was not published", got)
	}
	if !slices.Contains(s.client.Snapshot().ActiveRoles, ArtworkRole) {
		t.Fatal("stream end incorrectly deactivated the role")
	}
}

func TestDiscardedArtworkStillAccountsForEveryPart(t *testing.T) {
	s := mediaSession(t)
	s.client.active = s
	s.streams["artwork"] = true
	s.media.artworkChannels = s.client.cfg.Artwork
	s.applyArtwork(Artwork{Channel: 0, Timestamp: 1, Data: []byte{1, 2, 3}})
	announce := make([]byte, 14)
	announce[0] = 8
	announce[1] = 2
	binary.BigEndian.PutUint32(announce[10:], 4)
	if err := s.artwork(announce); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{8, 0, 10, 11}); err != nil {
		t.Fatal(err)
	}
	s.state.Available = false
	if err := s.artwork([]byte{8, 0, 12}); err != nil {
		t.Fatal(err)
	}
	if transfer := s.media.transfer; transfer == nil || transfer.received != 3 || !transfer.discard || transfer.image.Data != nil {
		t.Fatal("unavailable transfer retained data or lost accounting")
	}
	s.state.Available = true
	if err := s.artwork([]byte{8, 0, 13}); err != nil {
		t.Fatal(err)
	}
	if s.media.transfer != nil {
		t.Fatal("discarded transfer did not complete")
	}
	if image, ok := s.client.CurrentArtwork(0); !ok || image.Timestamp != 1 || !bytes.Equal(image.Data, []byte{1, 2, 3}) {
		t.Fatal("partly discarded image replaced the current one")
	}
	s.state.Available = false
	if err := s.artwork(announce); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{8, 0, 1, 2, 3, 4, 5}); err == nil {
		t.Fatal("unavailable transfer skipped size validation")
	}
	if err := s.artwork([]byte{8, 1}); err != nil {
		t.Fatal(err)
	}
	if s.media.transfer != nil {
		t.Fatal("unavailable transfer ignored cancellation")
	}
}

func TestCurrentArtworkConcurrentReaders(t *testing.T) {
	s := mediaSession(t)
	s.client.active = s
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				image, ok := s.client.CurrentArtwork(0)
				if !ok {
					continue
				}
				if len(image.Data) != 256 {
					t.Error("partial current image")
					return
				}
				for _, value := range image.Data {
					if value != image.Data[0] {
						t.Error("torn current image")
						return
					}
				}
				image.Data[0] ^= 0xff
			}
		})
	}
	for i := range 1000 {
		s.applyArtwork(Artwork{Channel: 0, Timestamp: int64(i), Data: bytes.Repeat([]byte{byte(i)}, 256)})
		if i%10 == 0 {
			s.applyArtwork(Artwork{Channel: 0})
		}
	}
	close(stop)
	readers.Wait()
}

func TestCurrentPositionUsesOnlyAppliedMetadataAndAnEstablishedClock(t *testing.T) {
	s := mediaSession(t)
	s.client.active = s
	s.state.Metadata = &Metadata{Timestamp: 0, Progress: &Progress{PositionMS: 3200, DurationMS: 9000, Speed: 0}}
	s.pendingMetadata = &Metadata{Timestamp: s.now() + 1000000, Progress: &Progress{PositionMS: 8000, DurationMS: 9000, Speed: 0}}
	s.publish()
	if _, ok := s.client.CurrentPosition(); ok {
		t.Fatal("position was reported before clock convergence")
	}
	for _, at := range []int64{1, 2, 3} {
		s.filter.Update(0, 100, at)
	}
	if position, ok := s.client.CurrentPosition(); !ok || position != 3200 {
		t.Fatal("position did not use the applied metadata", position, ok)
	}
	s.clearRole("metadata")
	s.publish()
	if _, ok := s.client.CurrentPosition(); ok {
		t.Fatal("removed metadata role retained a position")
	}
	s.client.active = nil
	if _, ok := s.client.CurrentPosition(); ok {
		t.Fatal("disconnected client retained a position")
	}
}

func TestTrackPositionCannotOverflowWireIntegerRanges(t *testing.T) {
	for _, tc := range []struct{ timestamp, now, position, duration, speed, want int64 }{
		{timestamp: math.MinInt64, now: math.MaxInt64, position: 100, duration: 9000, speed: math.MaxInt64, want: 9000},
		{timestamp: math.MinInt64, now: math.MaxInt64, position: 100, speed: math.MaxInt64, want: math.MaxInt64},
		{timestamp: math.MaxInt64, now: math.MinInt64, position: 100, speed: math.MaxInt64, want: 0},
		{position: math.MaxInt64, duration: math.MaxInt64 - 1, speed: 0, want: math.MaxInt64 - 1},
	} {
		metadata := Metadata{Timestamp: tc.timestamp, Progress: &Progress{PositionMS: tc.position, DurationMS: tc.duration, Speed: tc.speed}}
		if got, ok := metadata.Position(tc.now); !ok || got != tc.want {
			t.Fatalf("position %d (known=%t), want %d", got, ok, tc.want)
		}
	}
}
