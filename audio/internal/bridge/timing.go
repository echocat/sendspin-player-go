package bridge

import "math"

func timestamp(ns float64) int64 {
	if math.IsNaN(ns) || math.IsInf(ns, 0) || ns <= 0 || ns >= float64(math.MaxInt64) {
		return 0
	}
	return int64(ns)
}
func PWFrames(requested uint64, matched uint32, duration uint64, num, denom, rate, capacity uint32) uint32 {
	if capacity == 0 {
		return 0
	}
	if requested == 0 {
		requested = uint64(matched)
	}
	if requested == 0 {
		if duration == 0 || num == 0 || denom == 0 || rate == 0 || duration > math.MaxUint64/uint64(num) {
			return 0
		}
		frames := duration * uint64(num)
		if frames > math.MaxUint64/uint64(rate) {
			return 0
		}
		frames *= uint64(rate)
		if frames%uint64(denom) != 0 {
			return 0
		}
		requested = frames / uint64(denom)
	}
	return uint32(min(requested, uint64(capacity)))
}
func PWTime(now, delay int64, num, denom uint32, queued, buffered uint64, rate uint32) int64 {
	if now <= 0 || num == 0 || denom == 0 || rate == 0 || queued > uint64(rate)*2 || buffered > uint64(rate)*2 {
		return 0
	}
	return timestamp(float64(now) + float64(max(delay, 0))*1e9*float64(num)/float64(denom) + float64(queued+buffered)*1e9/float64(rate))
}
func WATime(submitted uint64, rate uint32, position, frequency, qpc, previous uint64) int64 {
	if rate == 0 || frequency == 0 || position == 0 || qpc == 0 || qpc > math.MaxInt64/100 || position < previous {
		return 0
	}
	queued := float64(submitted)/float64(rate) - float64(position)/float64(frequency)
	if queued < 0 || queued > 2 {
		return 0
	}
	return timestamp(float64(qpc)*100 + queued*1e9)
}
func CATime(host uint64, latency uint32, rate float64) int64 {
	if host == 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || float64(latency)/rate > 2 {
		return 0
	}
	return timestamp(float64(host) + float64(latency)*1e9/rate)
}
