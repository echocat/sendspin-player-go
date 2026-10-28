package sendspin

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"
)

func TestLocalTimestampConversionDoesNotWrapDuration(t *testing.T) {
	s := newSession(&Client{}, context.Background(), func() {})
	s.epoch = time.Unix(1700000000, 123456789)
	for _, timestamp := range []int64{0, 1, -1, 1 << 60, -(1 << 60), 10_000_000_000_000, -10_000_000_000_000} {
		want := time.Unix(s.epoch.Unix()+timestamp/1_000_000, int64(s.epoch.Nanosecond())+(timestamp%1_000_000-1)*1000)
		if got, err := s.local(timestamp); err != nil || !got.Equal(want) {
			t.Fatal("microsecond conversion wrapped a presentation deadline", timestamp, got, want, err)
		}
	}
}

func TestEpochMicrosecondsMatchesArbitraryPrecisionBoundaries(t *testing.T) {
	epoch := time.Now()
	boundary := int64(math.MaxInt64) / int64(time.Microsecond)
	for _, counter := range []int64{math.MinInt64, math.MinInt64 + 1, -boundary - 2, -boundary - 1, -boundary, -1, 0, 1, boundary, boundary + 1, boundary + 2, math.MaxInt64 - 1, math.MaxInt64} {
		// Independent big-integer Unix-nanosecond oracle, including floor
		// division for negative values; never multiply the input in int64.
		value := new(big.Int).Mul(big.NewInt(epoch.Unix()), big.NewInt(1_000_000_000))
		value.Add(value, big.NewInt(int64(epoch.Nanosecond())))
		span := new(big.Int).Sub(big.NewInt(counter), big.NewInt(1))
		value.Add(value, span.Mul(span, big.NewInt(1000)))
		seconds, nanos := new(big.Int), new(big.Int)
		seconds.DivMod(value, big.NewInt(1_000_000_000), nanos)
		want := time.Unix(seconds.Int64(), nanos.Int64())
		got, err := epochMicroseconds(epoch, counter)
		if err != nil || !got.Equal(want) {
			t.Fatal("counter conversion differs from exact oracle", counter, got, want, err)
		}
		if counter >= -boundary+1 && counter <= boundary+1 {
			if got != epoch.Add(time.Duration(counter-1)*time.Microsecond) {
				t.Fatal("ordinary mapping lost Go's monotonic clock", counter)
			}
		}
	}
}

func TestUnrepresentablePresentationTimestampNeverBecomesAnImmediateEvent(t *testing.T) {
	for _, role := range []string{"metadata", "color", "artwork", "visualizer"} {
		t.Run(role, func(t *testing.T) {
			s := mediaSession(t)
			var err error
			switch role {
			case "metadata":
				err = s.serverState([]byte(`{"metadata":{"timestamp":9223372036854775807,"title":"not now"}}`))
			case "color":
				err = s.serverState([]byte(`{"color":{"timestamp":9223372036854775807,"primary":[1,2,3]}}`))
			case "artwork":
				s = artworkTestSession(t)
				err = s.artwork(artworkAnnounce(0, math.MaxInt64, 0))
			case "visualizer":
				s.streams["visualizer"] = true
				s.media.visualizer = VisualizerConfig{Types: []string{"peak"}}
				frame := binary.BigEndian.AppendUint64([]byte{20}, math.MaxInt64)
				err = s.visualization(append(frame, 1))
			}
			if !errors.Is(err, errClockMapping) {
				t.Fatal("unrepresentable mapping was not rejected", err)
			}
			if s.state.Metadata != nil || s.state.Colors != nil || s.media.images[0] != nil || len(s.media.visualQueue) != 0 {
				t.Fatal("unrepresentable future timestamp became an applied presentation")
			}
		})
	}
}
