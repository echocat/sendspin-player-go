// Package render contains the bounded queue and lifecycle shared by the native
// desktop outputs. Native drivers own their callbacks, clocks and device handles.
package render

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	log "github.com/echocat/slf4g"
)

var (
	ErrUnavailable       = errors.New("native audio: backend unavailable on this platform/build")
	ErrTiming            = errors.New("native audio: missing, stale or invalid native timing")
	ErrClockReset  error = clockResetError{}
	ErrDriver            = errors.New("native audio: driver disconnected or invalidated the stream")
	ErrClosed            = errors.New("native audio: device closed")
)

type clockResetError struct{ flush bool }

func (clockResetError) Error() string {
	return "native audio: underrun changed the reserved output timeline"
}
func (e clockResetError) AudioTimelineReset() bool { return e.flush }
func (clockResetError) Is(target error) bool       { _, ok := target.(clockResetError); return ok }

// Driver methods are serialized by Device; Clock must be a pure native clock
// read, safe in a render callback. Stop quiesces callbacks/discards native queues;
// Close must join all callbacks before releasing their Go handle, even on error.
type Driver interface {
	Start() error
	Stop() error
	Close() error
	Check() error
	Clock() int64
}
type Factory func(*Device) (Driver, error)
type Config struct {
	Backend, Device                                 string
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

func (c Config) Normalize(f audio.Format) (Config, error) {
	if c.BufferDuration == 0 {
		c.BufferDuration = 100 * time.Millisecond
	}
	if c.StartupTimeout == 0 {
		c.StartupTimeout = 2 * time.Second
	}
	if c.MaxTimingWindow == 0 {
		c.MaxTimingWindow = 2 * time.Millisecond
	}
	if f.SampleRate < 8000 || f.SampleRate > 384000 || f.Channels < 1 || f.Channels > 8 || len(c.Device) > 4096 || strings.ContainsRune(c.Device, 0) || c.BufferDuration < 10*time.Millisecond || c.BufferDuration > 500*time.Millisecond || c.StartupTimeout < 10*time.Millisecond || c.StartupTimeout > 10*time.Second || c.MaxTimingWindow <= 0 || c.MaxTimingWindow > 50*time.Millisecond {
		return c, errors.New("native audio: invalid format or output configuration")
	}
	if c.Logger == nil {
		c.Logger = log.GetLoggerForCurrentPackage()
	}
	return c, nil
}

type Device struct {
	mu                      sync.Mutex
	driver                  Driver
	format                  audio.Format
	config                  Config
	ring                    []float32
	head, size              int
	running, started, ready bool
	fault                   error
	epoch, reserved         uint64
	hasReservation          bool
	end, tail               time.Time
	observed                time.Time
	changed                 chan struct{}
	op                      chan struct{}
	closed                  chan struct{}
	once                    sync.Once
	closeErr                error
	now                     func() time.Time
	diagnostics             Diagnostics
}

// Diagnostics is internal lifecycle-test metadata, never media or an accuracy
// measurement. It makes native-quantum/queue mismatches observable without logs
// or allocations in the render callback.
type Diagnostics struct {
	Callbacks, Underruns, Resets                        uint64
	LastFrames, MaxFrames, QueuedFrames, CapacityFrames int
	Epoch, Reserved                                     uint64
}

func (d *Device) Diagnostics() Diagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.diagnostics
	v.QueuedFrames = d.size / d.format.Channels
	v.CapacityFrames = len(d.ring) / d.format.Channels
	v.Epoch = d.epoch
	v.Reserved = d.reserved
	return v
}

func Open(ctx context.Context, c Config, f audio.Format, factory Factory) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	c, err = c.Normalize(f)
	if err != nil {
		return nil, err
	}
	frames := max(1, int(int64(f.SampleRate)*int64(c.BufferDuration)/int64(time.Second)))
	d := &Device{format: f, config: c, ring: make([]float32, frames*f.Channels), changed: make(chan struct{}, 1), op: make(chan struct{}, 1), closed: make(chan struct{}), now: monotime.Now}
	d.op <- struct{}{}
	d.driver, err = factory(d)
	if err != nil {
		if d.driver != nil {
			err = errors.Join(err, d.driver.Close())
		}
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, errors.Join(err, d.Close())
	}
	c.Logger.With("backend", c.Backend).Debug("Native playback configured")
	return d, nil
}

func (d *Device) MaxSamples() int      { return d.format.SampleRate * d.format.Channels / 2 }
func (d *Device) Format() audio.Format { return d.format }
func (d *Device) signal() {
	select {
	case d.changed <- struct{}{}:
	default:
	}
}

// Process fills native-owned interleaved float32 storage. Invalid timing never
// releases media to the device. Callback work is bounded, without sample-buffer
// allocation, native lifecycle calls, logging, network waits or channel waits.
func (d *Device) Process(out []float32, first int64) {
	clear(out)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.diagnostics.Callbacks++
	d.diagnostics.LastFrames = len(out) / d.format.Channels
	d.diagnostics.MaxFrames = max(d.diagnostics.MaxFrames, d.diagnostics.LastFrames)
	if !d.running || d.fault != nil {
		return
	}
	if len(out) == 0 || len(out) > d.MaxSamples() || len(out) > len(d.ring) || len(out)%d.format.Channels != 0 {
		d.failLocked(ErrDriver)
		return
	}
	before := d.now()
	native := d.driver.Clock()
	after := d.now()
	window := after.Sub(before)
	period := time.Duration(len(out)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate)
	if first <= 0 || native <= 0 || window < 0 || window > d.config.MaxTimingWindow || first < native-int64(period+d.config.MaxTimingWindow) || first > native+int64(2*time.Second) {
		// Inactive/startup streams can report no clock for their first cycles.
		// Only zeros can be queued before readiness; wait for genuine timing.
		if !d.ready && d.size == 0 {
			return
		}
		d.failLocked(ErrTiming)
		return
	}
	start := before.Add(window/2 + time.Duration(first-native))
	n := min(len(out), d.size)
	part := min(n, len(d.ring)-d.head)
	copy(out, d.ring[d.head:d.head+part])
	copy(out[part:n], d.ring[:n-part])
	d.head = (d.head + n) % len(d.ring)
	d.size -= n
	if n < len(out) {
		d.diagnostics.Underruns++
		d.epoch++
	}
	d.end = start.Add(time.Duration(len(out)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
	if d.end.After(d.tail) {
		d.tail = d.end
	}
	d.ready = true
	d.observed = after
	d.signal()
}
func (d *Device) failLocked(err error) {
	if d.fault == nil {
		d.fault = err
	}
	d.ready = false
	d.head, d.size = 0, 0
	d.signal()
}
func (d *Device) Fail(err error) { d.mu.Lock(); defer d.mu.Unlock(); d.failLocked(err) }
func (d *Device) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.closed:
		return ErrClosed
	case <-d.op:
	}
	if err := ctx.Err(); err != nil {
		d.unlock()
		return err
	}
	select {
	case <-d.closed:
		d.unlock()
		return ErrClosed
	default:
		return nil
	}
}
func (d *Device) unlock() { d.op <- struct{}{} }
func (d *Device) ensureStarted(ctx context.Context) error {
	d.mu.Lock()
	fault := d.fault
	d.mu.Unlock()
	if fault != nil {
		return fault
	}
	if !d.started {
		if err := ctx.Err(); err != nil {
			return err
		}
		d.mu.Lock()
		d.running = true
		d.mu.Unlock()
		if err := d.driver.Start(); err != nil {
			d.mu.Lock()
			d.running = false
			d.mu.Unlock()
			return err
		}
		d.started = true
	}
	timer := time.NewTimer(d.config.StartupTimeout)
	defer timer.Stop()
	health := time.NewTicker(100 * time.Millisecond)
	defer health.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.driver.Check(); err != nil {
			d.Fail(err)
			return err
		}
		d.mu.Lock()
		ready, fault := d.ready, d.fault
		observed := d.observed
		d.mu.Unlock()
		if fault != nil {
			return fault
		}
		if ready {
			if age := d.now().Sub(observed); age < 0 || age > time.Second {
				d.Fail(ErrTiming)
				return ErrTiming
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.closed:
			return ErrClosed
		case <-timer.C:
			return ErrTiming
		case <-d.changed:
		case <-health.C:
		}
	}
}
func (d *Device) Next(ctx context.Context) (time.Time, error) {
	if err := d.lock(ctx); err != nil {
		return time.Time{}, err
	}
	defer d.unlock()
	if err := d.ensureStarted(ctx); err != nil {
		return time.Time{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fault != nil {
		return time.Time{}, d.fault
	}
	d.reserved = d.epoch
	d.hasReservation = true
	return d.end.Add(time.Duration(d.size/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate)), nil
}
func (d *Device) Write(ctx context.Context, samples []float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(samples)%d.format.Channels != 0 {
		return errors.New("native audio: partial frame")
	}
	for i, value := range samples {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("native audio: nonfinite sample")
		}
	}
	if err := d.lock(ctx); err != nil {
		return err
	}
	defer d.unlock()
	if len(samples) == 0 {
		return ctx.Err()
	}
	if err := d.ensureStarted(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	epoch := d.epoch
	if d.hasReservation {
		epoch = d.reserved
	} else {
		d.reserved = epoch
		d.hasReservation = true
	}
	d.mu.Unlock()
	var health *time.Ticker
	accepted := false
	defer func() {
		if health != nil {
			health.Stop()
		}
	}()
	for len(samples) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-d.closed:
			return ErrClosed
		default:
		}
		d.mu.Lock()
		if d.fault != nil {
			err := d.fault
			d.mu.Unlock()
			return err
		}
		if epoch != d.epoch {
			d.diagnostics.Resets++
			d.mu.Unlock()
			return clockResetError{flush: accepted}
		}
		n := min(len(samples), len(d.ring)-d.size, 4096/d.format.Channels*d.format.Channels)
		tail := (d.head + d.size) % len(d.ring)
		for i, value := range samples[:n] {
			d.ring[(tail+i)%len(d.ring)] = max(-1, min(1, value))
		}
		d.size += n
		accepted = accepted || n > 0
		full := d.size == len(d.ring)
		d.mu.Unlock()
		samples = samples[n:]
		if len(samples) > 0 && full {
			if health == nil {
				health = time.NewTicker(100 * time.Millisecond)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-d.closed:
				return ErrClosed
			case <-d.changed:
			case <-health.C:
				if err := d.driver.Check(); err != nil {
					d.Fail(err)
					return err
				}
				d.mu.Lock()
				observed := d.observed
				d.mu.Unlock()
				if age := d.now().Sub(observed); age < 0 || age > time.Second {
					d.Fail(ErrTiming)
					return ErrTiming
				}
			}
		}
	}
	return nil
}
func (d *Device) stop(ctx context.Context, closing bool) error {
	d.mu.Lock()
	d.running = false
	d.ready = false
	d.hasReservation = false
	d.head, d.size = 0, 0
	tail := d.tail
	d.mu.Unlock()
	if d.started {
		if err := d.driver.Stop(); err != nil {
			return err
		}
		d.started = false
	}
	if wait := tail.Sub(d.now()); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		closed := d.closed
		if closing {
			closed = nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return ErrClosed
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	d.tail = time.Time{}
	if errors.Is(d.fault, ErrTiming) {
		d.fault = nil
	}
	d.mu.Unlock()
	return nil
}
func (d *Device) Flush(ctx context.Context) error {
	if err := d.lock(ctx); err != nil {
		return err
	}
	defer d.unlock()
	return d.stop(ctx, false)
}
func (d *Device) Close() error {
	d.once.Do(func() {
		close(d.closed)
		<-d.op
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		d.closeErr = errors.Join(d.stop(ctx, true), d.driver.Close())
		d.config.Logger.With("backend", d.config.Backend).Debug("Native playback closed")
	})
	return d.closeErr
}
