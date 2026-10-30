package alsa

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	log "github.com/echocat/slf4g"
)

var (
	ErrUnavailable  = errors.New("alsa: native output requires supported Linux and libasound")
	ErrPluginTiming = errors.New("alsa: non-hardware route requires explicit AllowPluginTiming")
	ErrTiming       = errors.New("alsa: invalid or unavailable native timing")
	ErrClockReset   = errors.New("alsa: PCM timing changed during a scheduled write")
	ErrClosed       = errors.New("alsa: output device closed")
	errAgain        = errors.New("alsa: native write would block")
	errInterrupted  = errors.New("alsa: native operation interrupted")
	errXrun         = errors.New("alsa: native PCM underrun")
)

var logger = log.GetLoggerForCurrentPackage()

// Output selects one ALSA playback PCM. Device defaults to "default"; it never
// changes the system default/mixer. A direct hardware device can be selected
// explicitly (for example hw:CARD=Device,DEV=0). No resampling is negotiated.
type Output struct {
	// SharedLibPath selects exactly this libasound file; empty uses libasound.so.2.
	// Explicit load/symbol failures never fall back to the default library.
	SharedLibPath  string
	Device         string
	BufferDuration time.Duration
	PeriodDuration time.Duration
	// MaxTimingWindow bounds the Go/native clock correlation call window, not
	// the driver's physical accuracy. Zero selects 2ms.
	MaxTimingWindow time.Duration
	// AllowPluginTiming acknowledges unverified downstream plugin/server timing
	// and stop guarantees. It is separate from opening a physical HW PCM.
	AllowPluginTiming bool
	Logger            log.Logger
}

// Info describes negotiated software/driver configuration, not hardware proof.
type Info struct {
	DriverType   string
	SampleFormat string
	SampleRate   int
	Channels     int
	BufferFrames int
	PeriodFrames int
	Approximate  bool
}

// Device is the optional ALSA-specific view of an audio.Device returned by Open.
// Info is immutable and safe to inspect concurrently with playback.
type Device interface {
	audio.Device
	Info() Info
}

type pcmState int

const (
	stateUnknown pcmState = iota
	stateSetup
	statePrepared
	stateRunning
	stateXrun
	stateSuspended
)

type nativeSnapshot struct {
	state      pcmState
	delay      int64
	stamp, now int64
}
type pcm interface {
	snapshot() (nativeSnapshot, error)
	write([]byte, int) (int, error)
	prepare() error
	start() error
	drop() error
	close() error
}
type negotiated struct {
	info     Info
	encoding sampleEncoding
	hardware bool
}
type nativeOpener func(context.Context, Output, audio.Format) (pcm, negotiated, error)

func (o Output) defaults() (Output, error) {
	if o.Device == "" {
		o.Device = "default"
	}
	if o.BufferDuration == 0 {
		o.BufferDuration = 50 * time.Millisecond
	}
	if o.PeriodDuration == 0 {
		o.PeriodDuration = 5 * time.Millisecond
	}
	if o.MaxTimingWindow == 0 {
		o.MaxTimingWindow = 2 * time.Millisecond
	}
	if len(o.Device) > 4096 || containsNUL(o.Device) || o.BufferDuration < 10*time.Millisecond || o.BufferDuration > 500*time.Millisecond || o.PeriodDuration < time.Millisecond || o.PeriodDuration > 100*time.Millisecond || o.PeriodDuration*2 > o.BufferDuration || o.MaxTimingWindow <= 0 || o.MaxTimingWindow > 50*time.Millisecond {
		return o, errors.New("alsa: invalid device/buffer/timing configuration")
	}
	if o.Logger == nil {
		o.Logger = logger
	}
	return o, nil
}
func containsNUL(s string) bool {
	for _, r := range s {
		if r == 0 {
			return true
		}
	}
	return false
}

// Open configures playback only. Native status starts lazily with a bounded
// silence pre-roll on Next/first Write; no guessed startup timestamp is returned.
func (o Output) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	return openWith(ctx, o, f, openNative)
}
func openWith(ctx context.Context, o Output, f audio.Format, open nativeOpener) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	o, err = o.defaults()
	if err != nil {
		return nil, err
	}
	if f.SampleRate < 8000 || f.SampleRate > 384000 || f.Channels < 1 || f.Channels > 32 {
		return nil, errors.New("alsa: invalid audio format")
	}
	p, config, err := open(ctx, o, f)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (audio.Device, error) { return nil, errors.Join(err, p.close()) }
	if !config.hardware && !o.AllowPluginTiming {
		return fail(ErrPluginTiming)
	}
	if config.info.BufferFrames <= 0 || config.info.BufferFrames > f.SampleRate*2 || config.info.PeriodFrames <= 0 || config.info.PeriodFrames > config.info.BufferFrames || config.encoding.bytes() == 0 {
		return fail(errors.New("alsa: invalid negotiated configuration"))
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	config.info.SampleRate, config.info.Channels = f.SampleRate, f.Channels
	config.info.Approximate = !config.hardware
	d := &device{pcm: p, config: config, output: o, format: f, gate: make(chan struct{}, 1), closed: make(chan struct{}), now: monotime.Now}
	d.buffer = make([]byte, config.info.PeriodFrames*f.Channels*config.encoding.bytes())
	d.gate <- struct{}{}
	o.Logger.With("hardware_pcm", config.hardware).Debug("ALSA playback configured")
	return d, nil
}

type device struct {
	pcm      pcm
	config   negotiated
	output   Output
	format   audio.Format
	gate     chan struct{}
	closed   chan struct{}
	once     sync.Once
	closeErr error
	now      func() time.Time
	timed    bool
	started  bool
	tail     time.Time
	stopErr  error
	buffer   []byte
}

func (d *device) Info() Info { return d.config.info }
func (d *device) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.closed:
		return ErrClosed
	case <-d.gate:
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
func (d *device) unlock() { d.gate <- struct{}{} }
func (d *device) wait(ctx context.Context, delay time.Duration, closing bool) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
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
		return ctx.Err()
	}
}

func (d *device) prime(ctx context.Context) error {
	d.timed = false
	if d.stopErr != nil {
		return d.stopErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.pcm.drop(); err != nil {
		return err
	}
	d.started = false
	if err := d.wait(ctx, d.tail.Sub(d.now()), false); err != nil {
		return err
	}
	d.tail = time.Time{}
	if err := d.pcm.prepare(); err != nil {
		return err
	}
	frames := d.config.info.BufferFrames
	block := min(frames, d.config.info.PeriodFrames)
	silence := make([]byte, block*d.format.Channels*d.config.encoding.bytes())
	for remaining := frames; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-d.closed:
			return ErrClosed
		default:
		}
		n, err := d.pcm.write(silence, min(remaining, block))
		if errors.Is(err, errInterrupted) {
			continue
		}
		if err != nil {
			return err
		}
		if n <= 0 || n > min(remaining, block) {
			return ErrTiming
		}
		remaining -= n
	}
	if err := d.pcm.start(); err != nil {
		return err
	}
	d.started = true
	return nil
}

func (d *device) readTiming() (time.Time, nativeSnapshot, error) {
	before := d.now()
	s, err := d.pcm.snapshot()
	after := d.now()
	if err != nil {
		return time.Time{}, s, err
	}
	window := after.Sub(before)
	if window < 0 || window > d.output.MaxTimingWindow || s.state != stateRunning || s.stamp <= 0 || s.now < s.stamp || s.now-s.stamp > int64(2*time.Second) || s.delay < 0 || s.delay > int64(d.format.SampleRate)*2 {
		return time.Time{}, s, ErrTiming
	}
	delay := time.Duration(s.delay) * time.Second / time.Duration(d.format.SampleRate)
	stamp := before.Add(window/2 + time.Duration(s.stamp-s.now) + delay)
	if stamp.After(d.tail) {
		d.tail = stamp
	}
	return stamp, s, nil
}

func (d *device) Next(ctx context.Context) (time.Time, error) {
	if err := d.lock(ctx); err != nil {
		return time.Time{}, err
	}
	defer d.unlock()
	stamp, s, err := d.readTiming()
	if err != nil && !errors.Is(err, ErrTiming) && !errors.Is(err, errXrun) {
		d.timed = false
		return time.Time{}, err
	}
	if errors.Is(err, errXrun) || (err == nil && s.delay == 0) || s.state == stateSetup || s.state == statePrepared || s.state == stateXrun {
		if err = d.prime(ctx); err != nil {
			return time.Time{}, err
		}
		stamp, _, err = d.readTiming()
	}
	if err != nil {
		d.timed = false
		return time.Time{}, err
	}
	if err = ctx.Err(); err != nil {
		return time.Time{}, err
	}
	d.timed = true
	return stamp, nil
}

func (d *device) Write(ctx context.Context, samples []float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(samples)%d.format.Channels != 0 {
		return errors.New("alsa: partial output frame")
	}
	if err := validateSamples(ctx, samples); err != nil {
		return err
	}
	if err := d.lock(ctx); err != nil {
		return err
	}
	defer d.unlock()
	if len(samples) == 0 {
		return ctx.Err()
	}
	if !d.timed {
		if err := d.prime(ctx); err != nil {
			return err
		}
		if _, _, err := d.readTiming(); err != nil {
			return err
		}
		d.timed = true
	}
	for len(samples) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-d.closed:
			return ErrClosed
		default:
		}
		frames := min(len(samples)/d.format.Channels, d.config.info.PeriodFrames)
		data := d.buffer[:frames*d.format.Channels*d.config.encoding.bytes()]
		encodeInto(data, samples[:frames*d.format.Channels], d.config.encoding)
		n, err := d.pcm.write(data, frames)
		if errors.Is(err, errAgain) || errors.Is(err, errInterrupted) {
			if err = d.wait(ctx, time.Millisecond, false); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			d.timed = false
			if errors.Is(err, errXrun) {
				return errors.Join(ErrClockReset, err)
			}
			return err
		}
		if n <= 0 || n > frames {
			d.timed = false
			return ErrTiming
		}
		d.tail = d.tail.Add(time.Duration(n) * time.Second / time.Duration(d.format.SampleRate))
		samples = samples[n*d.format.Channels:]
	}
	return nil
}

func (d *device) stop(ctx context.Context, closing bool) error {
	deadline := d.tail
	statusErr := d.stopErr
	if d.started {
		var s nativeSnapshot
		var next time.Time
		next, s, statusErr = d.readTiming()
		if errors.Is(statusErr, errXrun) || s.state == stateXrun || s.state == stateSetup || s.state == statePrepared {
			statusErr = nil
			next = time.Time{}
		}
		if statusErr == nil && next.After(deadline) {
			deadline = next
		}
	}
	d.timed = false
	dropErr := d.pcm.drop()
	if dropErr != nil {
		return errors.Join(statusErr, dropErr)
	}
	d.started = false
	d.tail = deadline
	d.stopErr = statusErr
	// The reported pre-drop tail bounds already-submitted output. Dropped ring
	// frames needn't play; conservatively wait through that tail before success.
	if statusErr == nil && !deadline.IsZero() {
		if err := d.wait(ctx, deadline.Sub(d.now()), closing); err != nil {
			return err
		}
		d.tail = time.Time{}
	}
	return statusErr
}
func (d *device) Flush(ctx context.Context) error {
	if err := d.lock(ctx); err != nil {
		return err
	}
	defer d.unlock()
	if err := d.stop(ctx, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.pcm.prepare()
}
func (d *device) Close() error {
	d.once.Do(func() {
		close(d.closed)
		<-d.gate
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		d.closeErr = errors.Join(d.stop(ctx, true), d.pcm.close())
		d.output.Logger.Debug("ALSA playback closed")
	})
	return d.closeErr
}
