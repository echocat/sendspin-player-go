package sendspin

import (
	"errors"
	"math"
	"time"
)

var errClockMapping = errors.New("sendspin: unusable or unrepresentable clock mapping")

func (s *session) local(timestamp int64) (time.Time, error) {
	local, ok := s.filter.CheckedClientTime(timestamp)
	if !ok {
		return time.Time{}, errClockMapping
	}
	return epochMicroseconds(s.epoch, local)
}

// The session counter starts at 1. Preserve Go's monotonic clock for ordinary
// deadlines; split seconds/subseconds outside time.Duration's ~292-year span.
// Subtracting the counter origin before splitting could itself overflow int64.
func epochMicroseconds(epoch time.Time, counter int64) (time.Time, error) {
	if counter >= math.MinInt64/int64(time.Microsecond)+1 && counter <= math.MaxInt64/int64(time.Microsecond)+1 {
		return epoch.Add(time.Duration(counter-1) * time.Microsecond), nil
	}
	seconds := counter / 1_000_000
	base := epoch.Unix()
	if seconds > 0 && base > math.MaxInt64-seconds || seconds < 0 && base < math.MinInt64-seconds {
		return time.Time{}, errClockMapping
	}
	nanoseconds := int64(epoch.Nanosecond()) + (counter%1_000_000-1)*1000
	return time.Unix(base+seconds, nanoseconds).In(epoch.Location()), nil
}
