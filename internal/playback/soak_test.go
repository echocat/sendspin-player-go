package playback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestSustainedQueuePressureKeepsExactBoundedOwnership(t *testing.T) {
	e := New(nil, 4096, func(int64) (time.Time, error) { return time.Time{}, nil })
	e.DecodedLimit = 8192
	for i := range 100_000 {
		if i%17 == 0 {
			e.Reset()
		}
		if err := e.Enqueue(Chunk{Format: audio.Format{SampleRate: 48000, Channels: 1}, Timestamp: int64(i) * 1000, Samples: make([]float32, 240), WireBytes: 493}); err != nil {
			t.Fatal(err)
		}
		s := e.Stats()
		if s.WireBytes < 0 || s.WireBytes > e.Capacity || s.DecodedBytes > e.DecodedLimit || s.Chunks*493 != s.WireBytes || s.Chunks*960 != s.DecodedBytes || s.PeakWireBytes > e.Capacity || s.PeakDecodedBytes > e.DecodedLimit {
			t.Fatal("unbounded/inconsistent software queue", s)
		}
	}
	e.Reset()
	if s := e.Stats(); s.Chunks != 0 || s.WireBytes != 0 || s.DecodedBytes != 0 || s.Generation == 0 {
		t.Fatal(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
