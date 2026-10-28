package monotime

import (
	"math"
	"math/big"
	"testing"
	"time"
)

func TestCounterConversionMatchesExactRationalAndSaturates(t *testing.T) {
	for _, ticks := range []int64{-1, 0, 1, 999, 10_000_001, math.MaxInt64} {
		for _, frequency := range []int64{-1, 0, 1, 10_000_000, 1_000_000_000, math.MaxInt64} {
			want := int64(0)
			if ticks > 0 && frequency > 0 {
				n := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(1_000_000_000))
				n.Quo(n, big.NewInt(frequency))
				if !n.IsInt64() {
					want = math.MaxInt64
				} else {
					want = n.Int64()
				}
			}
			if got := counterDuration(ticks, frequency); int64(got) != want {
				t.Fatal("counter conversion", ticks, frequency, got, want)
			}
		}
	}
}

func TestProcessClockIsMonotonicAndCorrelatesToRuntime(t *testing.T) {
	before := Now()
	previous := before
	for range 1000 {
		next := Now()
		if next.Before(previous) {
			t.Fatal("process clock moved backwards")
		}
		previous = next
	}
	time.Sleep(time.Millisecond)
	if Since(before) <= 0 || Until(before) >= 0 {
		t.Fatal("clock did not advance")
	}
	delta := time.Until(Now())
	if delta < -50*time.Millisecond || delta > 50*time.Millisecond {
		t.Fatal("clock correlation outside initialization bound", delta)
	}
	t.Log("clock source", Source())
}
