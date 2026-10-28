package timesync

import (
	"math"
	"testing"
)

func TestCheckedMappingMatchesReferenceInItsDefinedRange(t *testing.T) {
	for _, origin := range []int64{-1_000_000_000_000, 0, 1_000_000_000_000} {
		for _, ppm := range []int64{-200, -1, 0, 1, 200} {
			f := New()
			for i := int64(1); i <= 200; i++ {
				at := i * 1_000_000
				f.Update(origin+i*ppm, 100, at)
				for _, local := range []int64{at - 100_000, at, at + 100_000} {
					server, ok := f.CheckedServerTime(local)
					if !ok || server != f.ServerTime(local) {
						t.Fatal("checked server map changed reference arithmetic", origin, ppm, i, local, server)
					}
					back, ok := f.CheckedClientTime(server)
					if !ok || back != f.ClientTime(server) {
						t.Fatal("checked client map changed reference arithmetic", origin, ppm, i, server, back)
					}
				}
			}
		}
	}
}

func TestCheckedMappingRejectsNonfiniteOrReversedModels(t *testing.T) {
	for _, invalid := range []struct{ offset, drift float64 }{
		{math.NaN(), 0}, {math.Inf(1), 0}, {math.Inf(-1), 0}, {0, math.NaN()}, {0, math.Inf(1)}, {0, math.Inf(-1)}, {0, -1}, {0, -2},
	} {
		f := &Filter{count: 3, p00: 1, offset: invalid.offset, drift: invalid.drift, useDrift: true}
		if f.Ready() {
			t.Fatal("unusable model reported ready", invalid)
		}
		if _, ok := f.CheckedClientTime(100); ok {
			t.Fatal("unusable client mapping accepted", invalid)
		}
		if _, ok := f.CheckedServerTime(100); ok {
			t.Fatal("unusable server mapping accepted", invalid)
		}
	}
}

func TestCheckedMappingRejectsIntegerOverflowAndFloatBoundary(t *testing.T) {
	for _, test := range []struct {
		f     *Filter
		value int64
	}{
		{New(), math.MaxInt64}, // The nearest float64 is already 2^63.
		{&Filter{offset: -2048}, math.MaxInt64 - 1023},
		{&Filter{offset: 2048}, math.MinInt64},
	} {
		if _, ok := test.f.CheckedClientTime(test.value); ok {
			t.Fatal("unrepresentable local counter accepted", test.value)
		}
	}
	for _, test := range []struct {
		f     *Filter
		value int64
	}{
		{&Filter{offset: 2048}, math.MaxInt64 - 1023},
		{&Filter{offset: -2048}, math.MinInt64 + 1023},
		{&Filter{last: math.MaxInt64}, math.MinInt64},
		{&Filter{last: math.MinInt64}, math.MaxInt64},
		{&Filter{offset: float64(math.MaxInt64)}, 0},
	} {
		if _, ok := test.f.CheckedServerTime(test.value); ok {
			t.Fatal("server addition/difference overflow accepted", test.value)
		}
	}
	if at, ok := New().CheckedClientTime(math.MinInt64); !ok || at != math.MinInt64 {
		t.Fatal("exactly representable negative boundary rejected")
	}
	if at, ok := New().CheckedClientTime(math.MaxInt64 - 1023); !ok || at != math.MaxInt64-1023 {
		t.Fatal("last exactly representable positive counter rejected")
	}
}
