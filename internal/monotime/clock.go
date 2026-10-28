// Package monotime supplies one process-local time.Time domain for protocol
// measurements and output scheduling. Windows uses QPC because time.Now may be
// coarser than a loopback round trip; no positive RTT or uncertainty is invented.
package monotime

import (
	"math"
	"math/bits"
	"time"
)

func Since(at time.Time) time.Duration { return Now().Sub(at) }
func Until(at time.Time) time.Duration { return at.Sub(Now()) }

func counterDuration(ticks, frequency int64) time.Duration {
	if ticks <= 0 || frequency <= 0 {
		return 0
	}
	seconds, remainder := ticks/frequency, ticks%frequency
	if seconds > math.MaxInt64/int64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	hi, lo := bits.Mul64(uint64(remainder), uint64(time.Second))
	fraction, _ := bits.Div64(hi, lo, uint64(frequency))
	ns := seconds * int64(time.Second)
	if fraction > uint64(math.MaxInt64-ns) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ns + int64(fraction))
}
