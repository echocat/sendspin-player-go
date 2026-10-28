//go:build cgo

package portaudio

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	pa "github.com/gordonklaus/portaudio"
)

// Output uses the default native output device. SuggestedLatency is a driver
// hint; actual DAC timestamps, not the requested latency, drive synchronization.
type Output struct {
	SuggestedLatency time.Duration
	FramesPerBuffer  int
}

var library struct {
	sync.Mutex
	users int
}

func retain() error {
	library.Lock()
	defer library.Unlock()
	if library.users == 0 {
		if err := pa.Initialize(); err != nil {
			return err
		}
	}
	library.users++
	return nil
}
func release() error {
	library.Lock()
	defer library.Unlock()
	library.users--
	if library.users == 0 {
		return pa.Terminate()
	}
	return nil
}

type device struct {
	mu         sync.Mutex
	stream     *pa.Stream
	format     audio.Format
	ring       []float32
	head, size int
	dacEnd     time.Duration
	timed      bool
	notify     chan struct{}
	closed     bool
	timingWait time.Duration
}

func (o Output) Open(ctx context.Context, format audio.Format) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if format.SampleRate < 8000 || format.SampleRate > 384000 || format.Channels < 1 || format.Channels > 8 {
		return nil, errors.New("sendspin: invalid native audio format")
	}
	period := o.FramesPerBuffer
	if period == 0 {
		period = 128
	}
	if period < 16 || period > 8192 || o.SuggestedLatency < 0 {
		return nil, errors.New("sendspin: invalid PortAudio configuration")
	}
	if err := retain(); err != nil {
		return nil, err
	}
	defaultDevice, err := pa.DefaultOutputDevice()
	if err != nil {
		_ = release()
		return nil, err
	}
	latency := o.SuggestedLatency
	if latency == 0 {
		latency = defaultDevice.DefaultLowOutputLatency
	}
	d := &device{format: format, ring: make([]float32, period*format.Channels*8), notify: make(chan struct{}, 1), timingWait: max(time.Second, 2*time.Duration(period)*time.Second/time.Duration(format.SampleRate))}
	params := pa.StreamParameters{Output: pa.StreamDeviceParameters{Device: defaultDevice, Channels: format.Channels, Latency: latency}, SampleRate: float64(format.SampleRate), FramesPerBuffer: period}
	d.stream, err = pa.OpenStream(params, func(out []float32, info pa.StreamCallbackTimeInfo) { d.callback(out, info) })
	if err != nil {
		_ = release()
		return nil, err
	}
	if err = d.stream.Start(); err != nil {
		_ = d.stream.Close()
		_ = release()
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// The real-time callback never waits for the network or allocates a sample
// buffer. Underruns emit silence; Next exposes the new hardware timeline so the
// shared renderer drops stale samples instead of speeding them up.
func (d *device) callback(out []float32, info pa.StreamCallbackTimeInfo) {
	clear(out)
	d.mu.Lock()
	n := min(len(out), d.size)
	first := min(n, len(d.ring)-d.head)
	copy(out, d.ring[d.head:d.head+first])
	copy(out[first:n], d.ring[:n-first])
	d.head = (d.head + n) % len(d.ring)
	d.size -= n
	d.dacEnd = info.OutputBufferDacTime + time.Duration(len(out)/d.format.Channels)*time.Second/time.Duration(d.format.SampleRate)
	d.timed = info.OutputBufferDacTime > 0
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

func (d *device) awaitTiming(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	ready, closed := d.timed, d.closed
	d.mu.Unlock()
	if closed {
		return errors.New("sendspin: audio device closed")
	}
	if ready {
		return nil
	}
	wait := d.timingWait
	if wait <= 0 {
		wait = time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			return errors.New("sendspin: audio driver did not provide DAC timestamps within startup budget")
		case <-d.notify:
			d.mu.Lock()
			ready, closed = d.timed, d.closed
			d.mu.Unlock()
			if closed {
				return errors.New("sendspin: audio device closed")
			}
			if ready {
				return nil
			}
		}
	}
}

func (d *device) Next(ctx context.Context) (time.Time, error) {
	// Start and restart are asynchronous. Wait for genuine callback timing;
	// never substitute wall-clock/sample-counter estimates for native feedback.
	if err := d.awaitTiming(ctx); err != nil {
		return time.Time{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return time.Time{}, errors.New("sendspin: audio device closed")
	}
	if !d.timed {
		return time.Time{}, errors.New("sendspin: audio driver invalidated DAC timestamps")
	}
	// Stream.Time and callback DAC time use the same PortAudio clock. Sample
	// between two monotonic Go readings to reduce conversion-call bias.
	before := monotime.Now()
	native := d.stream.Time()
	after := monotime.Now()
	queued := time.Duration(d.size/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate)
	return before.Add(after.Sub(before)/2 + d.dacEnd - native + queued), nil
}
func (d *device) Write(ctx context.Context, samples []float32) error {
	if len(samples)%d.format.Channels != 0 {
		return errors.New("sendspin: partial output frame")
	}
	for len(samples) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return errors.New("sendspin: audio device closed")
		}
		n := min(len(samples), len(d.ring)-d.size)
		n -= n % d.format.Channels
		tail := (d.head + d.size) % len(d.ring)
		first := min(n, len(d.ring)-tail)
		copy(d.ring[tail:tail+first], samples[:first])
		copy(d.ring[:n-first], samples[first:n])
		d.size += n
		d.mu.Unlock()
		samples = samples[n:]
		if len(samples) > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-d.notify:
			}
		}
	}
	return nil
}
func (d *device) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.stream.Abort(); err != nil {
		return err
	}
	d.mu.Lock()
	d.head = 0
	d.size = 0
	d.timed = false
	d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.stream.Start()
}
func (d *device) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
	abortErr := d.stream.Abort()
	closeErr := d.stream.Close()
	return errors.Join(abortErr, closeErr, release())
}
