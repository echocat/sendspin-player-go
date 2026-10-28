package oto

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	native "github.com/ebitengine/oto/v3"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

var (
	ErrApproximateOnly = errors.New("sendspin: Oto requires explicit AllowApproximate; hardware timing and downstream flush are unavailable")
	ErrInUse           = errors.New("sendspin: Oto output is already in use")
	ErrContextFormat   = errors.New("sendspin: Oto process context has a different format or device buffer")
)

// Output is an explicitly opt-in best-effort audio.Output. Prefer a hardware-
// clock backend when accurate synchronization or immediate output clearing is
// required. The zero value refuses to open rather than inventing clock feedback.
type Output struct {
	AllowApproximate bool
	// BufferDuration requests Oto's device buffer; zero selects 40 ms.
	BufferDuration time.Duration
	// EstimatedDeviceLatency is a calibration estimate, not measured feedback.
	// Zero uses BufferDuration. It does not include the SDK/player software queue.
	EstimatedDeviceLatency time.Duration
	// PlayerBufferFrames bounds Oto's additional software buffer; zero selects 256.
	PlayerBufferFrames int
}

type options struct {
	buffer, latency time.Duration
	playerFrames    int
}

func (o Output) options(format audio.Format) (options, error) {
	if !o.AllowApproximate {
		return options{}, ErrApproximateOnly
	}
	if format.SampleRate < 8000 || format.SampleRate > 192000 || (format.Channels != 1 && format.Channels != 2) {
		return options{}, errors.New("sendspin: Oto supports mono/stereo at 8000-192000 Hz")
	}
	if o.BufferDuration == 0 {
		o.BufferDuration = 40 * time.Millisecond
	}
	if o.EstimatedDeviceLatency == 0 {
		o.EstimatedDeviceLatency = o.BufferDuration
	}
	if o.PlayerBufferFrames == 0 {
		o.PlayerBufferFrames = 256
	}
	if o.BufferDuration <= 0 || o.BufferDuration > time.Second || o.EstimatedDeviceLatency < 0 || o.EstimatedDeviceLatency > 5*time.Second || o.PlayerBufferFrames < 1 || o.PlayerBufferFrames > 8192 {
		return options{}, errors.New("sendspin: invalid Oto buffer/latency configuration")
	}
	return options{o.BufferDuration, o.EstimatedDeviceLatency, o.PlayerBufferFrames}, nil
}

type player interface {
	Play()
	PauseAndStopReading()
	SetBufferSize(int)
	BufferedSize() int
	Seek(int64, int) (int64, error)
	Err() error
}

type backend interface {
	newPlayer(io.Reader) player
	err() error
}

type otoBackend struct{ context *native.Context }

func (b otoBackend) newPlayer(r io.Reader) player { return b.context.NewPlayer(r) }
func (b otoBackend) err() error                   { return b.context.Err() }

type contextPool struct {
	mu             sync.Mutex
	started, inUse bool
	format         audio.Format
	buffer         time.Duration
	backend        backend
	ready          <-chan struct{}
	err            error
	create         func(audio.Format, time.Duration) (backend, <-chan struct{}, error)
}

func createContext(format audio.Format, buffer time.Duration) (backend, <-chan struct{}, error) {
	ctx, ready, err := native.NewContext(&native.NewContextOptions{SampleRate: format.SampleRate, ChannelCount: format.Channels, Format: native.FormatFloat32LE, BufferSize: buffer, ApplicationName: "Sendspin player"})
	if err != nil {
		return nil, nil, err
	}
	return otoBackend{ctx}, ready, nil
}

var contexts = contextPool{create: createContext}

func (p *contextPool) acquire(ctx context.Context, format audio.Format, buffer time.Duration) (backend, error) {
	p.mu.Lock()
	if p.inUse {
		p.mu.Unlock()
		return nil, ErrInUse
	}
	if p.started && (p.format != format || p.buffer != buffer) {
		p.mu.Unlock()
		return nil, ErrContextFormat
	}
	if !p.started {
		p.started, p.format, p.buffer = true, format, buffer
		p.backend, p.ready, p.err = p.create(format, buffer)
	}
	if p.err != nil {
		err := p.err
		p.mu.Unlock()
		return nil, err
	}
	p.inUse = true
	backend, ready := p.backend, p.ready
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		p.release()
		return nil, ctx.Err()
	case <-ready:
	}
	if err := backend.err(); err != nil {
		p.release()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		p.release()
		return nil, err
	}
	return backend, nil
}

func (p *contextPool) release() { p.mu.Lock(); p.inUse = false; p.mu.Unlock() }

func (o Output) Open(ctx context.Context, format audio.Format) (audio.Device, error) {
	return o.open(ctx, format, &contexts)
}

func (o Output) open(ctx context.Context, format audio.Format, pool *contextPool) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, err := o.options(format)
	if err != nil {
		return nil, err
	}
	backend, err := pool.acquire(ctx, format, options.buffer)
	if err != nil {
		return nil, err
	}
	source := newSource(format)
	device := &device{source: source, backend: backend, pool: pool, latency: options.latency, format: format}
	device.player = backend.newPlayer(source)
	device.player.SetBufferSize(options.playerFrames * format.Channels * 4)
	device.player.Play()
	if err = ctx.Err(); err != nil {
		_ = device.Close()
		return nil, err
	}
	return device, nil
}

type device struct {
	format  audio.Format
	latency time.Duration
	source  *source
	player  player
	backend backend
	pool    *contextPool
	closed  bool
}

func (d *device) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.closed {
		return errors.New("sendspin: Oto device closed")
	}
	if err := d.backend.err(); err != nil {
		return err
	}
	return d.player.Err()
}

// Next returns only an opt-in estimate, not hardware-clock feedback.
func (d *device) Next(ctx context.Context) (time.Time, error) {
	if err := d.check(ctx); err != nil {
		return time.Time{}, err
	}
	bytes := d.source.buffered() + d.player.BufferedSize()
	queued := time.Duration(bytes) * time.Second / time.Duration(d.format.SampleRate*d.format.Channels*4)
	return monotime.Now().Add(queued + d.latency), nil
}

func (d *device) Write(ctx context.Context, samples []float32) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	return d.source.write(ctx, samples, func() error { return d.check(ctx) })
}

// Flush clears both software queues. Already submitted downstream audio can
// remain audible: this is explicitly part of AllowApproximate, not a DAC flush.
func (d *device) Flush(ctx context.Context) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	d.player.PauseAndStopReading()
	_, err := d.player.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	d.player.Play()
	return nil
}

// Close stops source reads but retains the process-global Oto context. A
// downstream audio tail can remain; it cannot safely be removed by this API.
func (d *device) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	d.player.PauseAndStopReading()
	_, err := d.player.Seek(0, io.SeekStart)
	d.source.close()
	d.pool.release()
	return err
}
