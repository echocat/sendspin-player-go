package sendspin

import (
	"math"
	"math/bits"
)

// Position returns whole milliseconds from this applied metadata state. It
// evaluates the RC1 formula without float rounding or integer overflow, rounds
// down after clamping at zero, and saturates an unlimited duration at MaxInt64.
// Clock-map uncertainty is unchanged: serverTime is supplied by the caller.
func (m Metadata) Position(serverTime int64) (int64, bool) {
	if m.Progress == nil {
		return 0, false
	}
	p := m.Progress
	limit := int64(math.MaxInt64)
	if p.DurationMS > 0 {
		limit = p.DurationMS
	}
	negative := serverTime < m.Timestamp
	var span uint64
	if negative {
		span = uint64(m.Timestamp) - uint64(serverTime)
	} else {
		span = uint64(serverTime) - uint64(m.Timestamp)
	}
	speed := uint64(p.Speed)
	if p.Speed < 0 {
		speed = 0 - speed
		negative = !negative
	}
	high, low := bits.Mul64(span, speed)
	const denominator = 1_000_000
	// A quotient beyond uint64 cannot be brought into the signed result range
	// by adding any int64 base position, so clamp before division/conversion.
	if high >= denominator {
		if negative {
			return 0, true
		}
		return limit, true
	}
	whole, remainder := bits.Div64(high, low, denominator)
	if negative {
		// base - fraction: floor needs a borrow for a non-integral decrease.
		if remainder != 0 {
			if whole == math.MaxUint64 {
				return 0, true
			}
			whole++
		}
		if p.PositionMS <= 0 || whole >= uint64(p.PositionMS) {
			return 0, true
		}
		return int64(min(uint64(limit), uint64(p.PositionMS)-whole)), true
	}
	if p.PositionMS < 0 {
		magnitude := 0 - uint64(p.PositionMS)
		if whole <= magnitude {
			return 0, true
		}
		return int64(min(uint64(limit), whole-magnitude)), true
	}
	if p.PositionMS >= limit || whole >= uint64(limit-p.PositionMS) {
		return limit, true
	}
	return p.PositionMS + int64(whole), true
}
