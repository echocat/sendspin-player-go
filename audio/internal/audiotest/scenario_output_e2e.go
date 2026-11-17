//go:build e2e

package audiotest

import (
	"context"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

type scenarioOutputStats struct {
	Opens        int64 `json:"opens"`
	Closes       int64 `json:"closes"`
	Writes       int64 `json:"writes"`
	Frames       int64 `json:"frames"`
	SignalWrites int64 `json:"nonzero_writes"`
	Flushes      int64 `json:"flushes"`
	Failures     int64 `json:"unexpected_operation_errors"`
}

func (o *scenarioOutput) stats() scenarioOutputStats {
	return scenarioOutputStats{o.opens.Load(), o.closes.Load(), o.writes.Load(), o.frames.Load(), o.signalWrites.Load(), o.flushes.Load(), o.failures.Load()}
}

func scenarioPlaybackProgress(before, after scenarioOutputStats, decodedBefore, decodedAfter, frames int64) bool {
	return decodedAfter-decodedBefore >= frames && after.Frames-before.Frames >= frames && after.SignalWrites > before.SignalWrites
}

// Use the renderer's process clock (QPC on Windows), not a second time domain.
// This is only a software validation option, never a fallback for a failed ALSA open.
type scenarioClockOutput struct{}
type scenarioClockDevice struct{ next time.Time }

func (scenarioClockOutput) Open(context.Context, audio.Format) (audio.Device, error) {
	return &scenarioClockDevice{}, nil
}
func (d *scenarioClockDevice) Next(ctx context.Context) (time.Time, error) {
	if now := monotime.Now(); d.next.Before(now) {
		d.next = now
	}
	return d.next, ctx.Err()
}
func (d *scenarioClockDevice) Write(ctx context.Context, s []float32) error {
	d.next = d.next.Add(time.Duration(len(s)/2) * time.Second / 48000)
	if wait := monotime.Until(d.next) - 40*time.Millisecond; wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
func (d *scenarioClockDevice) Flush(context.Context) error { d.next = time.Time{}; return nil }
func (d *scenarioClockDevice) Close() error                { return nil }
