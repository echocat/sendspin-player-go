//go:build darwin && (amd64 || arm64)

package coreaudio

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/dynlib"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
)

type address struct{ Selector, Scope, Element uint32 }
type componentDescription struct{ Type, SubType, Manufacturer, Flags, Mask uint32 }
type streamDescription struct {
	Rate                                                                                    float64
	Format, Flags, BytesPerPacket, FramesPerPacket, BytesPerFrame, Channels, Bits, Reserved uint32
}
type callbackSpec struct{ Function, Token uintptr }
type audioTimestamp struct {
	SampleTime      float64
	HostTime        uint64
	RateScalar      float64
	WordClock       uint64
	SMPTE           [24]byte
	Flags, Reserved uint32
}
type audioBuffer struct {
	Channels, Bytes uint32
	Data            unsafe.Pointer
}
type bufferList struct {
	Count, Padding uint32
	First          audioBuffer
}
type translation struct {
	Input             unsafe.Pointer
	InputBytes, Pad1  uint32
	Output            unsafe.Pointer
	OutputBytes, Pad2 uint32
}

const scopeGlobal = 0x676c6f62
const scopeInput = 0x696e7074
const scopeOutput = 0x6f757470

// AudioUnitProperties.h; these scopes are not AudioObject property fourccs.
const (
	auGlobal               = 0
	auInput                = 1
	auOutput               = 2
	propertyStreamFormat   = 8
	propertyMaximumFrames  = 14
	propertyRenderCallback = 23
	propertyCurrentDevice  = 2000
	propertyEnableIO       = 2003
	propertyInputCallback  = 2005
)

type api struct {
	// unitMu serializes native creation with teardown, not callbacks. Once an
	// unsafe teardown occurs, the cached API is quarantined for the rest of the
	// process. Its existing closing tokens/pinned storage and framework handles
	// stay alive, but reconnects cannot allocate more units through this binding.
	// A Stop/Uninitialize error alone does not quarantine a successfully disposed
	// and joined unit. There is deliberately no software fallback or reset API.
	unitMu                                         sync.Mutex
	quarantineErr                                  error
	get                                            func(uint32, *address, uint32, unsafe.Pointer, *uint32, unsafe.Pointer) int32
	getSize                                        func(uint32, *address, uint32, unsafe.Pointer, *uint32) int32
	nanos                                          func(uint64) uint64
	find                                           func(uintptr, *componentDescription) uintptr
	newUnit                                        func(uintptr, *uintptr) int32
	set                                            func(uintptr, uint32, uint32, uint32, unsafe.Pointer, uint32) int32
	getUnit                                        func(uintptr, uint32, uint32, uint32, unsafe.Pointer, *uint32) int32
	render                                         func(uintptr, *uint32, *audioTimestamp, uint32, uint32, *bufferList) int32
	initialize, uninitialize, dispose, start, stop func(uintptr) int32
	reset                                          func(uintptr, uint32, uint32) int32
	stringNew                                      func(uintptr, string, uint32) uintptr
	release                                        func(uintptr)
}

var errQuarantined = errors.New("coreaudio: native API quarantined after unsafe teardown; process restart required")

// beginOpen leaves unitMu held on success, so no concurrent teardown can turn
// unsafe between the quarantine check and creation/registration of a new unit.
func (a *api) beginOpen() error {
	a.unitMu.Lock()
	if a.quarantineErr != nil {
		a.unitMu.Unlock()
		return a.quarantineErr
	}
	return nil
}

// quarantine is called only while unitMu is held. Retain the first cause rather
// than accumulating errors on every subsequent Close/Open attempt.
func (a *api) quarantine(cause error) error {
	if a.quarantineErr == nil {
		a.quarantineErr = fmt.Errorf("%w: %w", errQuarantined, cause)
	}
	return a.quarantineErr
}

type libraryKey struct{ Unit, Core, CF string }

var libraries = struct {
	sync.Mutex
	values map[libraryKey]*api
}{values: make(map[libraryKey]*api)}

func loadAPI(o Output) (*api, error) {
	unit, err := dynlib.Open(o.SharedLibPath, "/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox")
	if err != nil {
		return nil, err
	}
	core, err := dynlib.Open(o.CoreAudioLibPath, "/System/Library/Frameworks/CoreAudio.framework/CoreAudio")
	if err != nil {
		return nil, err
	}
	cf, err := dynlib.Open(o.CoreFoundationLibPath, "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	if err != nil {
		return nil, err
	}
	key := libraryKey{unit.Path, core.Path, cf.Path}
	libraries.Lock()
	defer libraries.Unlock()
	if a := libraries.values[key]; a != nil {
		return a, nil
	}
	a := &api{}
	for _, b := range []struct {
		lib  *dynlib.Library
		dst  any
		name string
	}{
		{core, &a.get, "AudioObjectGetPropertyData"}, {core, &a.nanos, "AudioConvertHostTimeToNanos"},
		{core, &a.getSize, "AudioObjectGetPropertyDataSize"},
		{unit, &a.getUnit, "AudioUnitGetProperty"}, {unit, &a.render, "AudioUnitRender"},
		{unit, &a.find, "AudioComponentFindNext"}, {unit, &a.newUnit, "AudioComponentInstanceNew"}, {unit, &a.set, "AudioUnitSetProperty"}, {unit, &a.initialize, "AudioUnitInitialize"}, {unit, &a.uninitialize, "AudioUnitUninitialize"}, {unit, &a.dispose, "AudioComponentInstanceDispose"}, {unit, &a.start, "AudioOutputUnitStart"}, {unit, &a.stop, "AudioOutputUnitStop"}, {unit, &a.reset, "AudioUnitReset"},
		{cf, &a.stringNew, "CFStringCreateWithCString"}, {cf, &a.release, "CFRelease"},
	} {
		if err = b.lib.Bind(b.dst, b.name); err != nil {
			return nil, err
		}
	}
	libraries.values[key] = a
	return a, nil
}
func caError(code int32) error {
	if code != 0 {
		return fmt.Errorf("coreaudio: native operation failed (OSStatus %d)", code)
	}
	return nil
}
func (a *api) getProperty(object, selector, scope uint32, value unsafe.Pointer, size uint32) error {
	addr := address{selector, scope, 0}
	want := size
	if err := caError(a.get(object, &addr, 0, nil, &size, value)); err != nil {
		return err
	}
	if size != want {
		return render.ErrDriver
	}
	return nil
}

type driver struct {
	api                  *api
	unit, token          uintptr
	device               uint32
	dev                  *render.Device
	rate                 float64
	latency              uint32
	scope                uint32
	streams              []uint32
	initialized, started bool
	closing              atomic.Bool
	active               atomic.Int64
	closeOnce            sync.Once
	closeErr             error
	released             bool
}

var callback struct {
	once    sync.Once
	pointer uintptr
}

func callbackPointer() uintptr {
	callback.once.Do(func() {
		callback.pointer = purego.NewCallback(func(id uintptr, _ *uint32, stamp *audioTimestamp, _ uint32, frames uint32, buffers *bufferList) int32 {
			p, ok := bridge.Value(id).(*driver)
			if !ok {
				return -50
			}
			p.active.Add(1)
			defer p.active.Add(-1)
			zero := func() {
				if buffers != nil && buffers.Count <= 8 {
					items := unsafe.Slice(&buffers.First, int(buffers.Count))
					for _, b := range items {
						if b.Data != nil && b.Bytes <= uint32(p.dev.MaxSamples()*4) {
							clear(unsafe.Slice((*byte)(b.Data), int(b.Bytes)))
						}
					}
				}
			}
			if p.closing.Load() {
				zero()
				return 0
			}
			if buffers == nil || buffers.Count != 1 || frames == 0 || frames > uint32(p.dev.Format().SampleRate/2) || buffers.First.Data == nil || buffers.First.Channels != uint32(p.dev.Format().Channels) || uint64(buffers.First.Bytes) < uint64(frames)*uint64(p.dev.Format().Channels)*4 {
				zero()
				p.dev.Fail(render.ErrDriver)
				return -50
			}
			var first int64
			if stamp != nil && stamp.Flags&2 != 0 {
				first = bridge.CATime(p.api.nanos(stamp.HostTime), p.latency, p.rate)
			}
			p.dev.Process(unsafe.Slice((*float32)(buffers.First.Data), int(frames)*p.dev.Format().Channels), first)
			return 0
		})
	})
	return callback.pointer
}

func openNative(o Output, d *render.Device) (render.Driver, error) {
	a, err := loadAPI(o)
	if err != nil {
		return nil, err
	}
	return openNativeWithAPI(o, d, a)
}

func openNativeWithAPI(o Output, d *render.Device, a *api) (render.Driver, error) {
	if err := a.beginOpen(); err != nil {
		return nil, err
	}
	defer a.unitMu.Unlock()
	if bridge.Now() <= 0 {
		return nil, render.ErrTiming
	}
	p := &driver{api: a, dev: d, scope: scopeOutput}
	fail := func(err error) (render.Driver, error) { return nil, errors.Join(err, p.closeLocked()) }
	if err := p.selectDevice(o.Device, 0x644f7574, d.Format().SampleRate); err != nil {
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
	disabled, enabled := uint32(0), uint32(1)
	if err := set(propertyEnableIO, auInput, 1, unsafe.Pointer(&disabled), 4); err != nil {
		return fail(err)
	}
	if err := set(propertyEnableIO, auOutput, 0, unsafe.Pointer(&enabled), 4); err != nil {
		return fail(err)
	}
	if err := set(propertyCurrentDevice, auGlobal, 0, unsafe.Pointer(&p.device), 4); err != nil {
		return fail(err)
	}
	format := streamDescription{Rate: float64(d.Format().SampleRate), Format: 0x6c70636d, Flags: 9, BytesPerPacket: uint32(d.Format().Channels * 4), FramesPerPacket: 1, BytesPerFrame: uint32(d.Format().Channels * 4), Channels: uint32(d.Format().Channels), Bits: 32}
	if err := set(propertyStreamFormat, auInput, 0, unsafe.Pointer(&format), uint32(unsafe.Sizeof(format))); err != nil {
		return fail(err)
	}
	maximum := uint32(min(d.Format().SampleRate/2, 32768))
	if err := set(propertyMaximumFrames, auGlobal, 0, unsafe.Pointer(&maximum), 4); err != nil {
		return fail(err)
	}
	p.token = bridge.Register(p)
	cb := callbackSpec{callbackPointer(), p.token}
	if err := set(propertyRenderCallback, auInput, 0, unsafe.Pointer(&cb), uint32(unsafe.Sizeof(cb))); err != nil {
		return fail(err)
	}
	if err := caError(a.initialize(p.unit)); err != nil {
		return fail(err)
	}
	p.initialized = true
	if err := p.verifyFormat(auInput, 0, format); err != nil {
		return fail(err)
	}
	return p, nil
}

func (p *driver) selectDevice(uid string, defaultSelector uint32, rate int) error {
	a := p.api
	if uid != "" {
		name := a.stringNew(0, uid, 0x08000100) // kCFStringEncodingUTF8
		if name == 0 {
			return errors.New("coreaudio: invalid device UID")
		}
		defer a.release(name)
		var selected uint32
		tr := translation{Input: unsafe.Pointer(&name), InputBytes: 8, Output: unsafe.Pointer(&selected), OutputBytes: 4}
		var pin runtime.Pinner
		pin.Pin(&name)
		pin.Pin(&selected)
		err := a.getProperty(1, 0x75696464, scopeGlobal, unsafe.Pointer(&tr), uint32(unsafe.Sizeof(tr))) // 'uidd'
		pin.Unpin()
		if err != nil {
			return err
		}
		p.device = selected
	} else if err := a.getProperty(1, defaultSelector, scopeGlobal, unsafe.Pointer(&p.device), 4); err != nil {
		return err
	}
	if p.device == 0 {
		return render.ErrDriver
	}
	var err error
	p.rate, p.latency, p.streams, err = p.timing()
	if err != nil {
		return err
	}
	// TN2091: AUHAL does PCM layout conversion, not sample-rate conversion.
	// Never change the device's nominal rate to satisfy the client.
	if p.rate != float64(rate) {
		return fmt.Errorf("coreaudio: client rate %d differs from device rate %g; AUHAL requires matching rates", rate, p.rate)
	}
	return nil
}

func (p *driver) timing() (float64, uint32, []uint32, error) {
	var rate float64
	var latency uint32
	a := p.api
	if err := a.getProperty(p.device, 0x6e737274, scopeGlobal, unsafe.Pointer(&rate), 8); err != nil {
		return 0, 0, nil, err
	}
	if err := a.getProperty(p.device, 0x6c746e63, p.scope, unsafe.Pointer(&latency), 4); err != nil {
		return 0, 0, nil, err
	}
	addr := address{0x73746d23, p.scope, 0} // kAudioDevicePropertyStreams ('stm#')
	var size uint32
	if err := caError(a.getSize(p.device, &addr, 0, nil, &size)); err != nil {
		return 0, 0, nil, err
	}
	if size == 0 || size%4 != 0 || size > 256*4 {
		return 0, 0, nil, render.ErrDriver
	}
	streams := make([]uint32, size/4)
	if err := a.getProperty(p.device, addr.Selector, p.scope, unsafe.Pointer(&streams[0]), size); err != nil {
		return 0, 0, nil, err
	}
	var streamLatency uint32
	for i, stream := range streams {
		var value uint32
		if err := a.getProperty(stream, 0x6c746e63, scopeGlobal, unsafe.Pointer(&value), 4); err != nil {
			return 0, 0, nil, err
		}
		// One interleaved timestamp cannot represent differing stream latencies.
		if i != 0 && value != streamLatency {
			return 0, 0, nil, render.ErrTiming
		}
		streamLatency = value
	}
	total := uint64(latency) + uint64(streamLatency)
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 8000 || rate > 384000 || total > uint64(rate*2) {
		return 0, 0, nil, render.ErrTiming
	}
	return rate, uint32(total), streams, nil
}

func (p *driver) verifyFormat(scope, bus uint32, want streamDescription) error {
	var got streamDescription
	size := uint32(unsafe.Sizeof(got))
	if err := caError(p.api.getUnit(p.unit, propertyStreamFormat, scope, bus, unsafe.Pointer(&got), &size)); err != nil {
		return err
	}
	if size != uint32(unsafe.Sizeof(got)) || got != want {
		return errors.New("coreaudio: AudioUnit did not preserve the requested interleaved float32 format")
	}
	return nil
}

func (p *driver) Check() error {
	if p.closing.Load() {
		return render.ErrClosed
	}
	var alive uint32
	if err := p.api.getProperty(p.device, 0x6c69766e, scopeGlobal, unsafe.Pointer(&alive), 4); err != nil {
		return err
	}
	if alive == 0 {
		return render.ErrDriver
	}
	rate, latency, streams, err := p.timing()
	if err != nil {
		return err
	}
	if rate != p.rate || latency != p.latency || !slices.Equal(streams, p.streams) {
		return render.ErrDriver
	}
	return nil
}
func (p *driver) Start() error {
	if err := p.Check(); err != nil {
		return err
	}
	// A failed start can still have partially activated the unit. Teardown must
	// attempt Stop in that case as well.
	p.started = true
	return caError(p.api.start(p.unit))
}
func (p *driver) Stop() error {
	var err error
	if p.started {
		err = caError(p.api.stop(p.unit))
		if err == nil {
			p.started = false
		}
	}
	if err != nil || p.unit == 0 || p.closing.Load() {
		return err
	}
	if err := p.joinCallbacks(); err != nil {
		return err
	}
	return caError(p.api.reset(p.unit, auGlobal, 0))
}
func (p *driver) Clock() int64 { return bridge.Now() }
func (p *driver) Close() error {
	p.api.unitMu.Lock()
	defer p.api.unitMu.Unlock()
	return p.closeLocked()
}

// closeLocked also serves partial-open cleanup, which already owns unitMu.
func (p *driver) closeLocked() error {
	p.closeOnce.Do(func() { p.closeErr = p.close() })
	return p.closeErr
}
func (p *driver) close() error {
	p.closing.Store(true)
	var result error
	var disposed int32
	if p.unit != 0 {
		if p.started {
			result = caError(p.api.stop(p.unit))
		}
		// In particular, never dispose a unit underneath AudioUnitRender, even
		// when Stop reports failure. Closing callbacks no longer render.
		if err := p.joinCallbacks(); err != nil {
			return errors.Join(result, p.api.quarantine(fmt.Errorf("callback join before native disposal failed: %w", err)))
		}
		if p.initialized {
			result = errors.Join(result, caError(p.api.uninitialize(p.unit)))
		}
		disposed = p.api.dispose(p.unit)
		result = errors.Join(result, caError(disposed))
	}
	if err := p.joinCallbacks(); err != nil {
		return errors.Join(result, p.api.quarantine(fmt.Errorf("callback join after native disposal failed: %w", err)))
	}
	// Disposal failure retains a closing token; late callbacks never render.
	if disposed == 0 {
		bridge.Free(p.token)
		p.unit = 0
		p.released = true
	} else {
		result = errors.Join(result, p.api.quarantine(caError(disposed)))
	}
	return result
}

func (p *driver) joinCallbacks() error {
	deadline := time.Now().Add(3 * time.Second)
	for p.active.Load() != 0 {
		if time.Now().After(deadline) {
			return render.ErrDriver
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}
