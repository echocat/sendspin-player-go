package timesync

import "math"

// mappingUsable adds an SDK readiness/scheduling guard, not a Kalman algorithm
// change. A nonfinite model or nonpositive clock rate cannot schedule output.
func (f *Filter) mappingUsable() bool {
	d := 0.0
	if f.useDrift {
		d = f.drift
	}
	return !math.IsNaN(f.offset) && !math.IsInf(f.offset, 0) && !math.IsNaN(d) && !math.IsInf(d, 0) && 1+d > 0
}

func roundedInt64(value float64) (int64, bool) {
	value = math.Round(value)
	// float64(MaxInt64) rounds to 2^63, which is already outside int64.
	if math.IsNaN(value) || math.IsInf(value, 0) || value >= float64(math.MaxInt64) || value < float64(math.MinInt64) {
		return 0, false
	}
	return int64(value), true
}

// CheckedClientTime has the same reference math in its defined range, but
// reports an unrepresentable/non-monotonic mapping rather than wrapping it.
func (f *Filter) CheckedClientTime(server int64) (int64, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if !f.mappingUsable() {
		return 0, false
	}
	return roundedInt64(f.clientTimeValue(server))
}

// CheckedServerTime protects the diagnostic position read model from overflow.
// It preserves the reference's integer difference/rounded-offset/addition order.
func (f *Filter) CheckedServerTime(local int64) (int64, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if !f.mappingUsable() {
		return 0, false
	}
	if f.last > 0 && local < math.MinInt64+f.last || f.last < 0 && local > math.MaxInt64+f.last {
		return 0, false
	}
	d := 0.0
	if f.useDrift {
		d = f.drift
	}
	offset, ok := roundedInt64(f.offset + d*float64(local-f.last))
	if !ok || offset > 0 && local > math.MaxInt64-offset || offset < 0 && local < math.MinInt64-offset {
		return 0, false
	}
	return local + offset, true
}
