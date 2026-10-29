package timesync

import (
	"math"
	"testing"
)

func TestSourceInverseIncludesDriftRatherThanOnlyOffset(t *testing.T) {
	for _, drift := range []float64{-0.0002, 0.0002} {
		f := &Filter{count: 3, p00: 1, offset: 123456789, drift: drift, last: 1_000_000, useDrift: true}
		for _, capture := range []int64{2_000_000, 100_000_000, 1_000_000_000} {
			server, ok := f.CheckedServerTime(capture)
			want := int64(math.Round(float64(capture)*(1+drift) + f.offset - drift*float64(f.last)))
			back, valid := f.CheckedClientTime(server)
			if !ok || !valid || server != want || back != capture || server == capture+int64(f.offset) {
				t.Fatal("source inverse lost drift", capture, server, want, back)
			}
		}
	}
}
