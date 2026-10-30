// Package capture owns bounded native-input queues. It contains no device
// discovery, gain/mixer policy, file recording or implicit capture activation.
package capture

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

type Config = render.Config
type Driver = render.Driver
type Factory func(*Device) (Driver, error)

var ErrDriver = render.ErrDriver
var ErrTiming = render.ErrTiming
var ErrClosed = render.ErrClosed

type discontinuity struct{ cause error }

func (e discontinuity) Error() string               { return e.cause.Error() }
func (e discontinuity) Unwrap() error               { return e.cause }
func (discontinuity) AudioInputDiscontinuity() bool { return true }

var ErrOverrun error = discontinuity{errors.New("native input: bounded capture queue overrun")}

type block struct {
	first           time.Time
	remaining, used int
}
type Device struct {
	mu                   sync.Mutex
	driver               Driver
	format               audio.Format
	config               Config
	ring                 []float32
	head, size           int
	blocks               []block
	blockHead, blockSize int
	running              bool
	fault                error
	startedAt            time.Time
	op                   chan struct{}
	changed              chan struct{}
	closed               chan struct{}
	once                 sync.Once
	closeErr             error
}

func Open(ctx context.Context, c Config, f audio.Format, factory Factory) (audio.InputDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	c, err = c.Normalize(f)
	if err != nil {
		return nil, err
	}
	frames := max(1, int(int64(f.SampleRate)*int64(c.BufferDuration)/int64(time.Second)))
	d := &Device{format: f, config: c, ring: make([]float32, frames*f.Channels), blocks: make([]block, frames), op: make(chan struct{}, 1), changed: make(chan struct{}, 1), closed: make(chan struct{})}
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
	return d, nil
}
func (d *Device) Format() audio.Format { return d.format }
func (d *Device) MaxSamples() int      { return d.format.SampleRate * d.format.Channels / 2 }
func (d *Device) signal() {
	select {
	case d.changed <- struct{}{}:
	default:
	}
}
func (d *Device) failLocked(err error) {
	if d.fault == nil {
		d.fault = err
	}
	d.head, d.size, d.blockHead, d.blockSize = 0, 0, 0, 0
	d.signal()
}
func (d *Device) Fail(err error) { d.mu.Lock(); defer d.mu.Unlock(); d.failLocked(err) }

// Process copies callback-owned input into bounded Go storage. Neither the
// native input nor its amplitudes are modified. Clock failures release no data.
func (d *Device) Process(input []float32, first int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running || d.fault != nil {
		return
	}
	if len(input) == 0 || len(input)%d.format.Channels != 0 || len(input) > d.MaxSamples() {
		d.failLocked(ErrDriver)
		return
	}
	if len(input) > len(d.ring)-d.size || d.blockSize == len(d.blocks) {
		d.failLocked(ErrOverrun)
		return
	}
	before := monotime.Now()
	now := d.driver.Clock()
	after := monotime.Now()
	window := after.Sub(before)
	if first <= 0 || now <= 0 || window < 0 || window > d.config.MaxTimingWindow || first < now-int64(2*time.Second) || first > now+int64(d.config.MaxTimingWindow) {
		// A native clock may be zero on its first cycle. No guessed timestamp.
		if first == 0 && d.blockSize == 0 {
			return
		}
		d.failLocked(discontinuity{ErrTiming})
		return
	}
	for _, v := range input {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < -1 || v > 1 {
			d.failLocked(ErrDriver)
			return
		}
	}
	start := before.Add(window/2 + time.Duration(first-now))
	at := (d.head + d.size) % len(d.ring)
	part := min(len(input), len(d.ring)-at)
	copy(d.ring[at:], input[:part])
	copy(d.ring, input[part:])
	d.size += len(input)
	i := (d.blockHead + d.blockSize) % len(d.blocks)
	d.blocks[i] = block{first: start, remaining: len(input)}
	d.blockSize++
	d.signal()
}
func (d *Device) Read(ctx context.Context, dst []float32) (int, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return 0, time.Time{}, err
	}
	if len(dst)%d.format.Channels != 0 {
		return 0, time.Time{}, errors.New("native input: partial destination frame")
	}
	if len(dst) == 0 {
		return 0, time.Time{}, nil
	}
	select {
	case <-ctx.Done():
		return 0, time.Time{}, ctx.Err()
	case <-d.closed:
		return 0, time.Time{}, ErrClosed
	case <-d.op:
	}
	defer func() { d.op <- struct{}{} }()
	select {
	case <-d.closed:
		return 0, time.Time{}, ErrClosed
	default:
	}
	if err := ctx.Err(); err != nil {
		return 0, time.Time{}, err
	}
	d.mu.Lock()
	running := d.running
	fault := d.fault
	d.mu.Unlock()
	if fault != nil {
		return 0, time.Time{}, fault
	}
	if !running {
		if err := d.driver.Check(); err != nil {
			return 0, time.Time{}, err
		}
		d.mu.Lock()
		d.running = true
		d.startedAt = monotime.Now()
		d.mu.Unlock()
		if err := d.driver.Start(); err != nil {
			d.Fail(err)
			return 0, time.Time{}, err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, time.Time{}, err
		}
		select {
		case <-d.closed:
			return 0, time.Time{}, ErrClosed
		default:
		}
		d.mu.Lock()
		if d.fault != nil {
			err := d.fault
			d.mu.Unlock()
			return 0, time.Time{}, err
		}
		if d.blockSize > 0 {
			b := &d.blocks[d.blockHead]
			n := min(len(dst), b.remaining)
			first := b.first.Add(time.Duration(b.used/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
			part := min(n, len(d.ring)-d.head)
			copy(dst, d.ring[d.head:d.head+part])
			copy(dst[part:n], d.ring[:n-part])
			clear(d.ring[d.head : d.head+part])
			clear(d.ring[:n-part])
			d.head = (d.head + n) % len(d.ring)
			d.size -= n
			b.remaining -= n
			b.used += n
			if b.remaining == 0 {
				*b = block{}
				d.blockHead = (d.blockHead + 1) % len(d.blocks)
				d.blockSize--
			}
			d.mu.Unlock()
			return n, first, nil
		}
		remaining := d.config.StartupTimeout - monotime.Since(d.startedAt)
		d.mu.Unlock()
		if remaining <= 0 {
			remaining = time.Second
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, time.Time{}, ctx.Err()
		case <-d.closed:
			timer.Stop()
			return 0, time.Time{}, ErrClosed
		case <-d.changed:
			timer.Stop()
		case <-timer.C:
			if err := d.driver.Check(); err != nil {
				d.Fail(err)
				return 0, time.Time{}, err
			}
			return 0, time.Time{}, ErrTiming
		}
	}
}
func (d *Device) Close() error {
	d.once.Do(func() {
		close(d.closed)
		<-d.op
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		d.closeErr = errors.Join(d.driver.Stop(), d.driver.Close())
		d.mu.Lock()
		clear(d.ring)
		clear(d.blocks)
		d.size, d.blockSize = 0, 0
		d.mu.Unlock()
	})
	return d.closeErr
}
