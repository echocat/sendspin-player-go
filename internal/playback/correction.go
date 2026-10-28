package playback

import "time"

const correctionSpan = 150 * time.Millisecond
const correctionBudget = correctionSpan / 200 // 0.5%, both signs combined.

type correctionEvent struct {
	end  time.Time
	cost time.Duration
}
type correctionWindow struct{ events []correctionEvent }

// allow conservatively charges an entire correction whenever any of it could
// intersect a 150-ms output interval. Inserted frames occupy an interval; drops
// are instantaneous. Charging both through the inserted-frame end also bounds
// sign changes and format/rate transitions without netting opposite corrections.
// Ceil division never underestimates a frame's duration. At 384 kHz there can
// be at most 288 live events; memory does not grow with stream length.
func (w *correctionWindow) allow(next time.Time, rate, requested int) int {
	cutoff := next.Add(-correctionSpan)
	kept := w.events[:0]
	var used time.Duration
	for _, event := range w.events {
		if !event.end.Before(cutoff) {
			kept = append(kept, event)
			used += event.cost
		}
	}
	w.events = kept
	frame := (time.Second + time.Duration(rate) - 1) / time.Duration(rate)
	n := min(requested, int((correctionBudget-used)/frame))
	if n <= 0 {
		return 0
	}
	cost := time.Duration(n) * frame
	w.events = append(w.events, correctionEvent{end: next.Add(cost), cost: cost})
	return n
}
