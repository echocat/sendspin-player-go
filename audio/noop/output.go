// Package noop provides an explicit, paced discard output. It opens no hardware,
// retains no samples, and offers no evidence about physical audio or timing.
// Native auto-detection never chooses it as a fallback.
package noop

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

var ErrClosed = errors.New("noop: device closed")

type Output struct{}
type device struct {
	mu      sync.Mutex
	format  audio.Format
	tail    time.Time
	changed chan struct{}
	closed  bool
}

func (Output) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.SampleRate < 8000 || f.SampleRate > 384000 || f.Channels < 1 || f.Channels > 8 {
		return nil, errors.New("noop: invalid format")
	}
	return &device{format: f, changed: make(chan struct{})}, nil
}
func (d *device) Next(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return time.Time{}, ErrClosed
	}
	now := monotime.Now()
	if d.tail.Before(now) {
		d.tail = now
	}
	return d.tail, nil
}
func (d *device) Write(ctx context.Context, s []float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(s)%d.format.Channels != 0 {
		return errors.New("noop: partial frame")
	}
	for _, v := range s {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < -1 || v > 1 {
			return errors.New("noop: invalid normalized sample")
		}
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	now := monotime.Now()
	if d.tail.Before(now) {
		d.tail = now
	}
	d.tail = d.tail.Add(time.Duration(len(s)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
	wait := monotime.Until(d.tail) - 50*time.Millisecond
	changed := d.changed
	d.mu.Unlock()
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if closed {
			return ErrClosed
		}
		return nil
	case <-timer.C:
		return ctx.Err()
	}
}
func (d *device) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	d.tail = time.Time{}
	close(d.changed)
	d.changed = make(chan struct{})
	return nil
}
func (d *device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		d.tail = time.Time{}
		close(d.changed)
	}
	return nil
}
