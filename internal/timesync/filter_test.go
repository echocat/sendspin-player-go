package timesync

import (
	"math"
	"math/big"
	"testing"
)

func TestOffsetDriftAndInverse(t *testing.T) {
	f := New()
	if f.Ready() || !math.IsInf(f.Error(), 1) {
		t.Fatal("new filter ready")
	}
	for i := int64(1); i <= 500; i++ {
		at := i * 100000
		f.Update(123456+at/10000, 100, at)
	}
	if !f.Ready() {
		t.Fatal("filter failed to converge")
	}
	for _, local := range []int64{50000000, 51000000, 100000000} {
		want := local + 123456 + local/10000
		got := f.ServerTime(local)
		if math.Abs(float64(got-want)) > 2 {
			t.Fatalf("drift got %d want %d", got, want)
		}
		if back := f.ClientTime(got); math.Abs(float64(back-local)) > 1 {
			t.Fatalf("inverse %d", back-local)
		}
	}
}
func TestInvalidSamplesDoNotChangeFilter(t *testing.T) {
	f := New()
	f.Update(42, 10, 100)
	before := f.ServerTime(200)
	for _, args := range [][3]int64{{100, 1, 100}, {100, 1, 99}, {100, 0, 101}, {100, -1, 102}} {
		f.Update(args[0], args[1], args[2])
	}
	if f.ServerTime(200) != before {
		t.Fatal("invalid sample changed filter")
	}
}
func TestSample(t *testing.T) {
	o, u, ok := Sample(1000, 2200, 2300, 1500)
	if !ok || o != 1000 || u != 200 {
		t.Fatal(o, u, ok)
	}
	for _, v := range [][4]int64{{1, 2, 1, 3}, {1, 2, 3, 1}, {1, 2, 100, 3}} {
		if _, _, ok := Sample(v[0], v[1], v[2], v[3]); ok {
			t.Fatal(v)
		}
	}
}

func exactSample(t1, t2, t3, t4 int64) (int64, int64, bool) {
	if t4 <= t1 || t3 < t2 {
		return 0, 0, false
	}
	clientSpan := new(big.Int).Sub(big.NewInt(t4), big.NewInt(t1))
	serverSpan := new(big.Int).Sub(big.NewInt(t3), big.NewInt(t2))
	uncertainty := new(big.Int).Sub(clientSpan, serverSpan)
	uncertainty.Quo(uncertainty, big.NewInt(2))
	if uncertainty.Sign() <= 0 || !uncertainty.IsInt64() {
		return 0, 0, false
	}
	offset := new(big.Int).Add(big.NewInt(t2), big.NewInt(t3))
	offset.Sub(offset, big.NewInt(t1))
	offset.Sub(offset, big.NewInt(t4))
	offset.Quo(offset, big.NewInt(2))
	if !offset.IsInt64() {
		return 0, 0, false
	}
	return offset.Int64(), uncertainty.Int64(), true
}

func TestSampleMatchesExactIntegerArithmetic(t *testing.T) {
	for _, sample := range [][4]int64{
		{1000, 1001, 1002, 1004}, // -0.5us must truncate to zero, not -1.
		{1000, 1002, 1003, 1004}, // +0.5us must truncate to zero, not +1.
		{math.MinInt64, 0, 0, math.MaxInt64},
		{1, math.MinInt64, math.MaxInt64, 10},
		{0, math.MinInt64, math.MinInt64, 10},
		{math.MinInt64, math.MaxInt64 - 1, math.MaxInt64, math.MinInt64 + 100},
		{1000, 2200, 2300, 1500},
	} {
		wantOffset, wantError, wantOK := exactSample(sample[0], sample[1], sample[2], sample[3])
		offset, uncertainty, ok := Sample(sample[0], sample[1], sample[2], sample[3])
		if offset != wantOffset || uncertainty != wantError || ok != wantOK {
			t.Errorf("%v: got (%d,%d,%t), want (%d,%d,%t)", sample, offset, uncertainty, ok, wantOffset, wantError, wantOK)
		}
	}
}

func FuzzSample(f *testing.F) {
	f.Add(int64(1000), int64(1001), int64(1002), int64(1004))
	f.Add(int64(math.MinInt64), int64(0), int64(0), int64(math.MaxInt64))
	f.Fuzz(func(t *testing.T, t1, t2, t3, t4 int64) {
		offset, uncertainty, ok := Sample(t1, t2, t3, t4)
		wantOffset, wantError, wantOK := exactSample(t1, t2, t3, t4)
		if offset != wantOffset || uncertainty != wantError || ok != wantOK {
			t.Fatalf("got (%d,%d,%t), want (%d,%d,%t)", offset, uncertainty, ok, wantOffset, wantError, wantOK)
		}
	})
}

func TestSampleDoesNotAllocate(t *testing.T) {
	var offset, uncertainty int64
	var ok bool
	if count := testing.AllocsPerRun(100, func() { offset, uncertainty, ok = Sample(1000, 2200, 2300, 1500) }); count != 0 {
		t.Fatalf("live NTP arithmetic allocated %g objects", count)
	}
	if !ok || offset != 1000 || uncertainty != 200 {
		t.Fatal("allocation probe lost the actual result", offset, uncertainty, ok)
	}
}
