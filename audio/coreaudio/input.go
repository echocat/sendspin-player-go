//go:build darwin && (amd64 || arm64)

package coreaudio

import (
	"context"
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	log "github.com/echocat/slf4g"
)

// Input configures an input-only AUHAL. Open initializes the unit but does not
// start capture; the first nonempty Read does. Device is a CoreAudio device UID.
// The requested sample rate must equal the device's current nominal rate.
type Input struct {
	SharedLibPath                                   string
	CoreAudioLibPath, CoreFoundationLibPath         string
	Device                                          string
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

func (i Input) Open(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	return capture.Open(ctx, capture.Config{Backend: "coreaudio", Device: i.Device, BufferDuration: i.BufferDuration, StartupTimeout: i.StartupTimeout, MaxTimingWindow: i.MaxTimingWindow, Logger: i.Logger}, f, func(d *capture.Device) (capture.Driver, error) {
		a, err := loadAPI(Output{SharedLibPath: i.SharedLibPath, CoreAudioLibPath: i.CoreAudioLibPath, CoreFoundationLibPath: i.CoreFoundationLibPath})
		if err != nil {
			return nil, err
		}
		return openNativeInput(i, d, a)
	})
}

type inputDriver struct {
	driver
	capture        *capture.Device
	maximum        uint32
	samples        []float32
	buffers        bufferList
	pins           runtime.Pinner
	reading        atomic.Bool
	hasSampleTime  bool
	nextSampleTime float64
	inputCloseOnce sync.Once
}

var inputCallback struct {
	once    sync.Once
	pointer uintptr
}

func inputCallbackPointer() uintptr {
	inputCallback.once.Do(func() { inputCallback.pointer = purego.NewCallback(captureCallback) })
	return inputCallback.pointer
}

func captureCallback(id uintptr, flags *uint32, stamp *audioTimestamp, bus, frames uint32, _ *bufferList) int32 {
	p, ok := bridge.Value(id).(*inputDriver)
	if !ok {
		return -50 // paramErr
	}
	p.active.Add(1)
	defer p.active.Add(-1)
	if p.closing.Load() {
		return 0
	}
	if !p.reading.CompareAndSwap(false, true) {
		p.capture.Fail(capture.ErrDriver)
		return -50
	}
	defer p.reading.Store(false)
	if bus != 1 || frames == 0 || frames > p.maximum || stamp == nil || flags == nil {
		p.capture.Fail(capture.ErrDriver)
		return -50
	}
	channels := p.capture.Format().Channels
	n := int(frames) * channels
	p.buffers.Count = 1
	p.buffers.First = audioBuffer{Channels: uint32(channels), Bytes: uint32(n * 4), Data: unsafe.Pointer(&p.samples[0])}
	if code := p.api.render(p.unit, flags, stamp, 1, frames, &p.buffers); code != 0 {
		p.capture.Fail(caError(code))
		return code
	}
	if p.buffers.Count != 1 || p.buffers.First.Channels != uint32(channels) || p.buffers.First.Bytes != uint32(n*4) || p.buffers.First.Data != unsafe.Pointer(&p.samples[0]) {
		p.capture.Fail(capture.ErrDriver)
		return -50
	}
	// AudioTimeStamp sample time exposes dropped/discontinuous native blocks.
	if stamp.Flags&1 != 0 {
		if math.IsNaN(stamp.SampleTime) || math.IsInf(stamp.SampleTime, 0) || (p.hasSampleTime && math.Abs(stamp.SampleTime-p.nextSampleTime) > 0.5) {
			p.capture.Fail(inputDiscontinuity{capture.ErrTiming})
			return -50
		}
		p.hasSampleTime = true
		p.nextSampleTime = stamp.SampleTime + float64(frames)
	} else if p.hasSampleTime {
		p.capture.Fail(inputDiscontinuity{capture.ErrTiming})
		return -50
	}
	var first int64
	if stamp.Flags&2 != 0 {
		first = inputTime(p.api.nanos(stamp.HostTime), p.latency, p.rate)
	}
	if *flags&(1<<4) != 0 { // kAudioUnitRenderAction_OutputIsSilence
		clear(p.samples[:n])
	}
	p.capture.Process(p.samples[:n], first)
	return 0
}

type inputDiscontinuity struct{ error }

func (inputDiscontinuity) AudioInputDiscontinuity() bool { return true }
func (e inputDiscontinuity) Unwrap() error               { return e.error }

func inputTime(host uint64, latency uint32, rate float64) int64 {
	if host == 0 || host > math.MaxInt64 || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || float64(latency)/rate > 2 {
		return 0
	}
	// The input-only AUHAL timestamp already accounts for the buffer/safety
	// offset. Subtract only device + stream hardware latency to reach the ADC.
	// AudioConvertHostTimeToNanos and bridge.Now share mach_absolute_time's epoch.
	delay := uint64(float64(latency) * 1e9 / rate)
	if host <= delay {
		return 0
	}
	return int64(host - delay)
}

func openNativeInput(i Input, d *capture.Device, a *api) (capture.Driver, error) {
	if err := a.beginOpen(); err != nil {
		return nil, err
	}
	defer a.unitMu.Unlock()
	if bridge.Now() <= 0 {
		return nil, capture.ErrTiming
	}
	p := &inputDriver{driver: driver{api: a, scope: scopeInput}, capture: d, maximum: uint32(min(d.Format().SampleRate/2, 32768))}
	fail := func(err error) (capture.Driver, error) { return nil, errors.Join(err, p.closeLocked()) }
	if err := p.selectDevice(i.Device, 0x64496e20, d.Format().SampleRate); err != nil { // kAudioHardwarePropertyDefaultInputDevice ('dIn ')
		return fail(err)
	}
	description := componentDescription{Type: 0x61756f75, SubType: 0x6168616c, Manufacturer: 0x6170706c}
	component := a.find(0, &description)
	if component == 0 {
		return fail(render.ErrUnavailable)
	}
	if err := caError(a.newUnit(component, &p.unit)); err != nil {
		return fail(err)
	}
	set := func(property, scope, bus uint32, value unsafe.Pointer, size uint32) error {
		return caError(a.set(p.unit, property, scope, bus, value, size))
	}
	enabled, disabled := uint32(1), uint32(0)
	if err := set(propertyEnableIO, auInput, 1, unsafe.Pointer(&enabled), 4); err != nil {
		return fail(err)
	}
	if err := set(propertyEnableIO, auOutput, 0, unsafe.Pointer(&disabled), 4); err != nil {
		return fail(err)
	}
	if err := set(propertyCurrentDevice, auGlobal, 0, unsafe.Pointer(&p.device), 4); err != nil {
		return fail(err)
	}
	f := d.Format()
	format := streamDescription{Rate: float64(f.SampleRate), Format: 0x6c70636d, Flags: 9, BytesPerPacket: uint32(f.Channels * 4), FramesPerPacket: 1, BytesPerFrame: uint32(f.Channels * 4), Channels: uint32(f.Channels), Bits: 32}
	if err := set(propertyStreamFormat, auOutput, 1, unsafe.Pointer(&format), uint32(unsafe.Sizeof(format))); err != nil {
		return fail(err)
	}
	if err := set(propertyMaximumFrames, auGlobal, 0, unsafe.Pointer(&p.maximum), 4); err != nil {
		return fail(err)
	}
	// Retained Go storage is explicitly pinned, including the nested Data pointer.
	// AUHAL is never given the inputDriver pointer; only a registry integer token.
	p.samples = make([]float32, int(p.maximum)*f.Channels)
	p.pins.Pin(&p.samples[0])
	p.pins.Pin(&p.buffers)
	p.token = bridge.Register(p)
	cb := callbackSpec{inputCallbackPointer(), p.token}
	if err := set(propertyInputCallback, auGlobal, 0, unsafe.Pointer(&cb), uint32(unsafe.Sizeof(cb))); err != nil {
		return fail(err)
	}
	if err := caError(a.initialize(p.unit)); err != nil {
		return fail(err)
	}
	p.initialized = true
	if err := p.verifyFormat(auOutput, 1, format); err != nil {
		return fail(err)
	}
	return p, nil
}

func (p *inputDriver) Close() error {
	p.api.unitMu.Lock()
	defer p.api.unitMu.Unlock()
	return p.closeLocked()
}

func (p *inputDriver) closeLocked() error {
	p.inputCloseOnce.Do(func() {
		p.closeErr = p.driver.closeLocked()
		if p.released {
			p.pins.Unpin()
			p.samples = nil
			p.buffers = bufferList{}
		}
	})
	return p.closeErr
}
