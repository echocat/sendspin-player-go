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
