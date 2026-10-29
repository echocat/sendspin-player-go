//go:build e2e

package audiotest

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

type scenarioOutput struct {
	base                                                           audio.Output
	opens, closes, writes, frames, signalWrites, flushes, failures atomic.Int64
}
type scenarioDevice struct {
	audio.Device
	owner    *scenarioOutput
	once     sync.Once
	closeErr error
}
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
func (o *scenarioOutput) record(err error) error {
	if err != nil && !errors.Is(err, context.Canceled) {
		o.failures.Add(1)
	}
	return err
}
func (o *scenarioOutput) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	if f.SampleRate != 48000 || f.Channels != 2 {
		return nil, o.record(errors.New("unexpected scenario output format"))
	}
	d, err := o.base.Open(ctx, f)
	if err != nil {
		return nil, o.record(err)
	}
	o.opens.Add(1)
	return &scenarioDevice{Device: d, owner: o}, nil
}
func (d *scenarioDevice) Next(ctx context.Context) (time.Time, error) {
	at, err := d.Device.Next(ctx)
	return at, d.owner.record(err)
}
func (d *scenarioDevice) Write(ctx context.Context, s []float32) error {
	signal := false
	if len(s)%2 != 0 {
		return d.owner.record(errors.New("partial scenario frame"))
	}
	for i, v := range s {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 0.01004 || (i%2 == 1 && v != 0) {
			return d.owner.record(errors.New("scenario output exceeds approved weak left-only profile"))
		}
		signal = signal || v != 0
	}
	if err := d.Device.Write(ctx, s); err != nil {
		return d.owner.record(err)
	}
	d.owner.writes.Add(1)
	d.owner.frames.Add(int64(len(s) / 2))
	if signal {
		d.owner.signalWrites.Add(1)
	}
	return nil
}
func (d *scenarioDevice) Flush(ctx context.Context) error {
	err := d.Device.Flush(ctx)
	if err == nil {
		d.owner.flushes.Add(1)
	}
	return d.owner.record(err)
}
func (d *scenarioDevice) Close() error {
	d.once.Do(func() { d.closeErr = d.Device.Close(); d.owner.closes.Add(1); _ = d.owner.record(d.closeErr) })
	return d.closeErr
}

// Same bounded wall-clock fake used conceptually by the root E2E tests. This
// is only a software validation option, never a fallback for a failed ALSA open.
type scenarioClockOutput struct{}
type scenarioClockDevice struct{ next time.Time }

func (scenarioClockOutput) Open(context.Context, audio.Format) (audio.Device, error) {
	return &scenarioClockDevice{}, nil
}
func (d *scenarioClockDevice) Next(ctx context.Context) (time.Time, error) {
	if d.next.Before(time.Now()) {
		d.next = time.Now()
	}
	return d.next, ctx.Err()
}
func (d *scenarioClockDevice) Write(ctx context.Context, s []float32) error {
	d.next = d.next.Add(time.Duration(len(s)/2) * time.Second / 48000)
	if wait := time.Until(d.next) - 40*time.Millisecond; wait > 0 {
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
