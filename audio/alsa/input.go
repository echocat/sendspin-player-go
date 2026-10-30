//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	log "github.com/echocat/slf4g"
	"golang.org/x/sys/unix"
)

// Input captures an explicitly opened ALSA PCM. It does not alter any mixer or
// monitoring control. Device defaults to the configured direct hardware "hw".
type Input struct {
	Device, SharedLibPath                                           string
	BufferDuration, PeriodDuration, StartupTimeout, MaxTimingWindow time.Duration
	AllowPluginTiming                                               bool
	Logger                                                          log.Logger
}
type inputDevice struct {
	pcm      *nativePCM
	format   audio.Format
	config   negotiated
	options  Input
	data     []byte
	op       chan struct{}
	closed   chan struct{}
	started  bool
	once     sync.Once
	closeErr error
}

func (o Input) Open(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	return o.open(ctx, f, "")
}
func (o Input) open(ctx context.Context, f audio.Format, private string) (audio.InputDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.SampleRate < 8000 || f.SampleRate > 384000 || f.Channels < 1 || f.Channels > 8 {
		return nil, errors.New("alsa input: invalid format")
	}
	if o.Device == "" {
		o.Device = "hw"
	}
	if o.StartupTimeout == 0 {
		o.StartupTimeout = 2 * time.Second
	}
	if o.StartupTimeout < 10*time.Millisecond || o.StartupTimeout > 10*time.Second {
		return nil, errors.New("alsa input: invalid startup timeout")
	}
	out, err := (Output{Device: o.Device, SharedLibPath: o.SharedLibPath, BufferDuration: o.BufferDuration, PeriodDuration: o.PeriodDuration, MaxTimingWindow: o.MaxTimingWindow, AllowPluginTiming: o.AllowPluginTiming, Logger: o.Logger}).defaults()
	if err != nil {
		return nil, err
	}
	o.MaxTimingWindow = out.MaxTimingWindow
	p, n, err := openNativeDirection(ctx, out, f, private, 1)
	if err != nil {
		return nil, err
	}
	if !n.hardware && !o.AllowPluginTiming {
		return nil, errors.Join(ErrPluginTiming, p.close())
	}
	n.info.SampleRate = f.SampleRate
	n.info.Channels = f.Channels
	n.info.Approximate = !n.hardware
	dev := &inputDevice{pcm: p.(*nativePCM), format: f, config: n, options: o, data: make([]byte, min(n.info.BufferFrames, 2048)*f.Channels*n.encoding.bytes()), op: make(chan struct{}, 1), closed: make(chan struct{})}
	dev.op <- struct{}{}
	if err = ctx.Err(); err != nil {
		return nil, errors.Join(err, dev.Close())
	}
	return dev, nil
}
func (d *inputDevice) Info() Info { return d.config.info }
func (d *inputDevice) Read(ctx context.Context, dst []float32) (int, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return 0, time.Time{}, err
	}
	if len(dst)%d.format.Channels != 0 {
		return 0, time.Time{}, errors.New("alsa input: partial destination frame")
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
	if !d.started {
		if err := d.pcm.start(); err != nil {
			return 0, time.Time{}, err
		}
		d.started = true
	}
	deadline := monotime.Now().Add(d.options.StartupTimeout)
	frames := min(len(dst)/d.format.Channels, len(d.data)/(d.format.Channels*d.config.encoding.bytes()))
	for {
		if err := ctx.Err(); err != nil {
			return 0, time.Time{}, err
		}
		before := monotime.Now()
		s, err := d.pcm.snapshot()
		after := monotime.Now()
		if errors.Is(err, errXrun) || s.state == stateXrun {
			return 0, time.Time{}, capture.ErrOverrun
		}
		if err != nil {
			return 0, time.Time{}, err
		}
		if s.state != stateRunning || s.delay < 0 || s.delay > int64(d.format.SampleRate)*2 {
			return 0, time.Time{}, ErrTiming
		}
		var n int64
		if s.delay > 0 {
			n = d.pcm.api.read(d.pcm.handle, &d.data[0], uint64(min(int64(frames), s.delay)))
		} else {
			n = -int64(unix.EAGAIN)
		}
		if n > 0 {
			if n > int64(frames) || s.stamp <= 0 || s.now < s.stamp || after.Sub(before) < 0 || after.Sub(before) > d.options.MaxTimingWindow {
				return 0, time.Time{}, ErrTiming
			}
			firstNative := s.stamp - s.delay*int64(time.Second)/int64(d.format.SampleRate)
			if firstNative <= 0 {
				return 0, time.Time{}, ErrTiming
			}
			first := before.Add(after.Sub(before)/2 + time.Duration(firstNative-s.now))
			count := int(n) * d.format.Channels
			if err = decodeInput(dst[:count], d.data[:count*d.config.encoding.bytes()], d.config.encoding); err != nil {
				clear(dst[:count])
				return 0, time.Time{}, err
			}
			return count, first, nil
		}
		if n < 0 {
			err = d.pcm.api.err("read", int32(n))
			if errors.Is(err, errXrun) {
				return 0, time.Time{}, capture.ErrOverrun
			}
			if !errors.Is(err, errAgain) && !errors.Is(err, errInterrupted) {
				return 0, time.Time{}, err
			}
		}
		if monotime.Now().After(deadline) {
			return 0, time.Time{}, ErrTiming
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, time.Time{}, ctx.Err()
		case <-d.closed:
			timer.Stop()
			return 0, time.Time{}, ErrClosed
		case <-timer.C:
		}
	}
}
func (d *inputDevice) Close() error {
	d.once.Do(func() { close(d.closed); <-d.op; d.closeErr = errors.Join(d.pcm.drop(), d.pcm.close()); clear(d.data) })
	return d.closeErr
}
func decodeInput(dst []float32, b []byte, e sampleEncoding) error {
	for i := range dst {
		at := i * e.bytes()
		var v float32
		switch e {
		case encodingFloat:
			v = math.Float32frombits(binary.LittleEndian.Uint32(b[at:]))
		case encodingS32:
			v = float32(int32(binary.LittleEndian.Uint32(b[at:]))) / 2147483648
		case encodingS24:
			v = float32(int32(binary.LittleEndian.Uint32(b[at:]))<<8>>8) / 8388608
		case encodingS24Packed:
			value := uint32(b[at]) | uint32(b[at+1])<<8 | uint32(b[at+2])<<16
			v = float32(int32(value<<8)>>8) / 8388608
		case encodingS16:
			v = float32(int16(binary.LittleEndian.Uint16(b[at:]))) / 32768
		default:
			return errors.New("alsa input: unknown encoding")
		}
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < -1 || v > 1 {
			return errors.New("alsa input: invalid normalized sample")
		}
		dst[i] = v
	}
	return nil
}
