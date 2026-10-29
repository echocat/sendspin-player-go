//go:build windows && (amd64 || arm64)

package wasapi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/dynlib"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"golang.org/x/sys/windows"
)

var enumeratorID = windows.GUID{Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c, Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}}
var enumeratorIID = windows.GUID{Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35, Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}}
var clientIID = windows.GUID{Data1: 0x1cb9ad4c, Data2: 0xdbfa, Data3: 0x4c32, Data4: [8]byte{0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2}}
var renderIID = windows.GUID{Data1: 0xf294acfc, Data2: 0x3146, Data3: 0x4483, Data4: [8]byte{0xa7, 0xbf, 0xad, 0xdc, 0xa7, 0xc2, 0x60, 0xe2}}
var clockIID = windows.GUID{Data1: 0xcd63314f, Data2: 0x3fba, Data3: 0x4a1b, Data4: [8]byte{0x81, 0x2c, 0xef, 0x96, 0x35, 0x87, 0x28, 0xe7}}
var captureIID = windows.GUID{Data1: 0xc8adbd64, Data2: 0xe71e, Data3: 0x48a0, Data4: [8]byte{0xa4, 0xde, 0x18, 0x5c, 0x39, 0x5c, 0xd3, 0x17}}

const (
	streamEventCallback  = 0x00040000
	streamLoopback       = 0x00020000
	streamAutoConvert    = 0x80000000
	streamSRCQuality     = 0x08000000
	streamNoPersist      = 0x00080000
	bufferDiscontinuity  = 1
	bufferSilent         = 2
	bufferTimestampError = 4
	captureBufferEmpty   = 0x08890001 // AUDCLNT_S_BUFFER_EMPTY (success status)
	commandStart         = 1
	commandStop          = 2
)

type comObject struct{ Methods *[15]uintptr }

//go:uintptrescapes
func invoke(object unsafe.Pointer, index int, args ...uintptr) int32 {
	if object == nil || index < 0 || index >= 15 || len(args) > 15 {
		return -2147467261
	}
	var argv [16]uintptr
	argv[0] = uintptr(object)
	copy(argv[1:], args)
	methods := (*comObject)(object).Methods
	if methods == nil {
		return -2147467261
	}
	method := methods[index]
	if method == 0 {
		return -2147467261
	}
	r, _, _ := syscall.SyscallN(method, argv[:len(args)+1]...)
	runtime.KeepAlive(object)
	return int32(r)
}
func release(object unsafe.Pointer) {
	if object != nil {
		_ = invoke(object, 2)
	}
}
func waError(hr int32) error {
	if hr < 0 {
		return fmt.Errorf("wasapi: native operation failed (HRESULT 0x%08x)", uint32(hr))
	}
	return nil
}

type api struct {
	initialize   func(uintptr, uint32) int32
	uninitialize func()
	create       func(*windows.GUID, uintptr, uint32, *windows.GUID, *unsafe.Pointer) int32
}

func loadAPI(path string) (*api, error) {
	lib, err := dynlib.Open(path, "ole32.dll")
	if err != nil {
		return nil, err
	}
	a := &api{}
	for _, b := range []struct {
		dst  any
		name string
	}{{&a.initialize, "CoInitializeEx"}, {&a.uninitialize, "CoUninitialize"}, {&a.create, "CoCreateInstance"}} {
		if err = lib.Bind(b.dst, b.name); err != nil {
			return nil, err
		}
	}
	return a, nil
}
func waveFormat(rate, channels int) ([40]byte, error) {
	var b [40]byte
	if rate < 8000 || rate > 384000 {
		return b, errors.New("wasapi: unsupported sample rate")
	}
	var mask uint32
	switch channels {
	case 1:
		mask = 4
	case 2:
		mask = 3
	case 4:
		mask = 0x33
	case 6:
		mask = 0x3f
	case 8:
		mask = 0x63f
	default:
		return b, errors.New("wasapi: unsupported speaker layout")
	}
	binary.LittleEndian.PutUint16(b[0:], 0xfffe)
	binary.LittleEndian.PutUint16(b[2:], uint16(channels))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*channels*4))
	binary.LittleEndian.PutUint16(b[12:], uint16(channels*4))
	binary.LittleEndian.PutUint16(b[14:], 32)
	binary.LittleEndian.PutUint16(b[16:], 22)
	binary.LittleEndian.PutUint16(b[18:], 32)
	binary.LittleEndian.PutUint32(b[20:], mask)
	copy(b[24:], []byte{3, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71})
	return b, nil
}

type failure struct{ err error }
type command struct {
	kind  int
	reply chan error
}

// Both shared queues accept only borrowed buffers: Process copies capture input
// and fills render output synchronously. COM never retains a Go media pointer.
type nativeDevice interface {
	Format() audio.Format
	MaxSamples() int
	Process([]float32, int64)
	Fail(error)
}
type driver struct {
	api                      *api
	dev                      nativeDevice
	capture, loopback        bool
	endpoint                 *uint16
	format                   [40]byte
	commandEvent, audioEvent windows.Handle
	commands                 chan command
	ready                    chan error
	done                     chan struct{}
	shutdown                 chan struct{}
	requestMu                sync.Mutex
	readyOnce, closeOnce     sync.Once
	failure                  atomic.Pointer[failure]
	closeErr                 error
}

func (p *driver) fail(err error) {
	if err != nil {
		if p.failure.CompareAndSwap(nil, &failure{err}) {
			p.dev.Fail(err)
		}
	}
}
func (p *driver) signalReady(err error) { p.readyOnce.Do(func() { p.ready <- err }) }
func openNative(o Output, d *render.Device) (render.Driver, error) {
	p, err := openDriver(o.SharedLibPath, o.Device, false, false, d)
	if err != nil {
		return nil, err
	}
	return p, nil
}
func openDriver(path, endpoint string, capture, loopback bool, d nativeDevice) (*driver, error) {
	format, err := waveFormat(d.Format().SampleRate, d.Format().Channels)
	if err != nil {
		return nil, err
	}
	a, err := loadAPI(path)
	if err != nil {
		return nil, err
	}
	if bridge.Now() <= 0 {
		return nil, render.ErrTiming
	}
	p := &driver{api: a, dev: d, capture: capture, loopback: loopback, format: format, commands: make(chan command, 1), ready: make(chan error, 1), done: make(chan struct{}), shutdown: make(chan struct{})}
	if endpoint != "" {
		p.endpoint, err = windows.UTF16PtrFromString(endpoint)
		if err != nil {
			return nil, err
		}
	}
	p.commandEvent, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	p.audioEvent, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(p.commandEvent)
		return nil, err
	}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := p.worker()
		p.fail(err)
		p.signalReady(err)
		close(p.done)
	}()
	if err = <-p.ready; err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}
func (p *driver) worker() (err error) {
	if err = waError(p.api.initialize(0, 0)); err != nil {
		return err
	}
	defer p.api.uninitialize()
	var enumerator, device, client, renderer, clock, capturer unsafe.Pointer
	running := false
	defer func() {
		if running {
			err = errors.Join(err, waError(invoke(client, 11)))
		}
		release(clock)
		release(capturer)
		release(renderer)
		release(client)
		release(device)
		release(enumerator)
	}()
	if err = waError(p.api.create(&enumeratorID, 0, 23, &enumeratorIID, &enumerator)); err != nil {
		return err
	}
	if p.endpoint != nil {
		err = waError(invoke(enumerator, 5, uintptr(unsafe.Pointer(p.endpoint)), uintptr(unsafe.Pointer(&device))))
	} else {
		var flow uintptr // eRender for output and explicit loopback
		if p.capture && !p.loopback {
			flow = 1 // eCapture
		}
		err = waError(invoke(enumerator, 4, flow, 1, uintptr(unsafe.Pointer(&device))))
	}
	if err != nil {
		return err
	}
	if err = waError(invoke(device, 3, uintptr(unsafe.Pointer(&clientIID)), 23, 0, uintptr(unsafe.Pointer(&client)))); err != nil {
		return err
	}
	// Shared, event-driven, native rate conversion; no session volume persistence.
	flags := uintptr(streamEventCallback | streamAutoConvert | streamSRCQuality | streamNoPersist)
	if p.capture && p.loopback {
		flags |= streamLoopback
	}
	if err = waError(invoke(client, 3, 0, flags, 500000, 0, uintptr(unsafe.Pointer(&p.format[0])), 0)); err != nil {
		return err
	}
	var capacity uint32
	if err = waError(invoke(client, 4, uintptr(unsafe.Pointer(&capacity)))); err != nil {
		return err
	}
	if capacity == 0 || capacity > uint32(p.dev.Format().SampleRate/2) {
		return render.ErrDriver
	}
	if err = waError(invoke(client, 13, uintptr(p.audioEvent))); err != nil {
		return err
	}
	var frequency uint64
	var captureState captureTimeline
	var silence []float32
	if p.capture {
		if err = waError(invoke(client, 14, uintptr(unsafe.Pointer(&captureIID)), uintptr(unsafe.Pointer(&capturer)))); err != nil {
			return err
		}
		silence = make([]float32, int(capacity)*p.dev.Format().Channels)
	} else {
		if err = waError(invoke(client, 14, uintptr(unsafe.Pointer(&renderIID)), uintptr(unsafe.Pointer(&renderer)))); err != nil {
			return err
		}
		if err = waError(invoke(client, 14, uintptr(unsafe.Pointer(&clockIID)), uintptr(unsafe.Pointer(&clock)))); err != nil {
			return err
		}
		if err = waError(invoke(clock, 3, uintptr(unsafe.Pointer(&frequency)))); err != nil {
			return err
		}
		if frequency == 0 {
			return render.ErrTiming
		}
	}
	var submitted, previous uint64
	p.signalReady(nil)
	for {
		if err := p.Check(); err != nil {
			return err
		}
		select {
		case <-p.shutdown:
			return nil
		default:
		}
		// A finite wait also permits shutdown when SetEvent itself fails. Close
		// joins the worker before closing either handle or releasing COM.
		which, e := windows.WaitForMultipleObjects([]windows.Handle{p.commandEvent, p.audioEvent}, false, 100)
		if e != nil {
			return e
		}
		if which == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		if which == windows.WAIT_OBJECT_0 {
			var cmd command
			select {
			case cmd = <-p.commands:
			default:
				continue
			}
			var result error
			if cmd.kind == commandStart && !running {
				result = waError(invoke(client, 12))
				if !p.capture {
					var out unsafe.Pointer
					if result == nil {
						result = waError(invoke(renderer, 3, uintptr(capacity), uintptr(unsafe.Pointer(&out))))
					}
					if result == nil {
						result = waError(invoke(renderer, 4, uintptr(capacity), bufferSilent))
					}
				}
				submitted = uint64(capacity)
				previous = 0
				captureState = captureTimeline{}
				if result == nil {
					result = waError(invoke(client, 10))
					if result == nil {
						running = true
					}
				}
			} else if cmd.kind == commandStop {
				if running {
					result = waError(invoke(client, 11))
					if result == nil {
						running = false
					}
				}
				if result == nil {
					result = waError(invoke(client, 12))
				}
			}
			cmd.reply <- result
			if result != nil {
				return result
			}
			continue
		}
		if which != windows.WAIT_OBJECT_0+1 {
			return render.ErrDriver
		}
		if !running {
			continue
		}
		if p.capture {
			if err = p.capturePackets(capturer, capacity, silence, &captureState); err != nil {
				return err
			}
			continue
		}
		var padding uint32
		if err = waError(invoke(client, 6, uintptr(unsafe.Pointer(&padding)))); err != nil {
			return err
		}
		if padding > capacity {
			return render.ErrDriver
		}
		frames := capacity - padding
		if frames == 0 {
			continue
		}
		var position, qpc uint64
		hr := invoke(clock, 4, uintptr(unsafe.Pointer(&position)), uintptr(unsafe.Pointer(&qpc)))
		if err = waError(hr); err != nil {
			return err
		}
		if previous > 0 && (hr != 0 || position == 0 || qpc == 0) {
			// Only startup may lack a clock. Once media has a timeline, a
			// lost/invalid clock must not silently create an unrelated one.
			return render.ErrTiming
		}
		var first int64
		if hr == 0 && position > 0 && qpc > 0 {
			first = bridge.WATime(submitted, uint32(p.dev.Format().SampleRate), position, frequency, qpc, previous)
			if first <= 0 {
				return render.ErrTiming
			}
			previous = position
		}
		var out unsafe.Pointer
		if err = waError(invoke(renderer, 3, uintptr(frames), uintptr(unsafe.Pointer(&out)))); err != nil {
			return err
		}
		if out == nil {
			_ = invoke(renderer, 4, uintptr(frames), bufferSilent)
			return render.ErrDriver
		}
		var releaseFlags uintptr = bufferSilent
		if first > 0 {
			p.dev.Process(unsafe.Slice((*float32)(out), int(frames)*p.dev.Format().Channels), first)
			releaseFlags = 0
		}
		if err = waError(invoke(renderer, 4, uintptr(frames), releaseFlags)); err != nil {
			return err
		}
		submitted += uint64(frames)
	}
}
func (p *driver) request(kind int) error {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	if kind == commandStart {
		if err := p.Check(); err != nil {
			return err
		}
	}
	cmd := command{kind, make(chan error, 1)}
	stopped := func() error {
		if err := p.Check(); err != nil {
			return err
		}
		if kind == commandStart {
			return render.ErrClosed
		}
		return nil
	}
	select {
	case <-p.shutdown:
		return stopped()
	case <-p.done:
		return stopped()
	default:
	}
	select {
	case <-p.shutdown:
		return stopped()
	case <-p.done:
		return stopped()
	case p.commands <- cmd:
	}
	if err := windows.SetEvent(p.commandEvent); err != nil {
		p.fail(err)
		return err
	}
	select {
	case err := <-cmd.reply:
		return err
	case <-p.done:
		return stopped()
	}
}
func (p *driver) Start() error { return p.request(commandStart) }
func (p *driver) Stop() error  { return p.request(commandStop) }
func (p *driver) Check() error {
	if f := p.failure.Load(); f != nil {
		return f.err
	}
	return nil
}
func (p *driver) Clock() int64 { return bridge.Now() }
func (p *driver) Close() error {
	p.closeOnce.Do(func() {
		close(p.shutdown)
		err := windows.SetEvent(p.commandEvent)
		<-p.done
		p.requestMu.Lock()
		defer p.requestMu.Unlock()
		p.closeErr = errors.Join(err, p.Check(), windows.CloseHandle(p.audioEvent), windows.CloseHandle(p.commandEvent))
	})
	return p.closeErr
}

type inputDiscontinuity struct{ cause error }

func (e inputDiscontinuity) Error() string {
	return "wasapi: capture discontinuity: " + e.cause.Error()
}
func (e inputDiscontinuity) Unwrap() error               { return e.cause }
func (inputDiscontinuity) AudioInputDiscontinuity() bool { return true }

type captureTimeline struct {
	valid            bool
	endPosition, qpc uint64
}

// WASAPI returns capture QPC timestamps already converted to 100 ns units,
// not raw QPC ticks. Never synthesize the timestamp of a missing first packet.
func (s *captureTimeline) timestamp(frames, flags uint32, position, qpc uint64) (int64, error) {
	if flags & ^uint32(bufferDiscontinuity|bufferSilent|bufferTimestampError) != 0 {
		return 0, inputDiscontinuity{render.ErrDriver}
	}
	if flags&bufferDiscontinuity != 0 {
		return 0, inputDiscontinuity{render.ErrDriver}
	}
	if flags&bufferTimestampError != 0 || qpc > math.MaxInt64/100 {
		return 0, inputDiscontinuity{render.ErrTiming}
	}
	if qpc == 0 && !s.valid {
		return 0, nil
	}
	if qpc == 0 || (s.valid && (position != s.endPosition || qpc <= s.qpc)) {
		return 0, inputDiscontinuity{render.ErrTiming}
	}
	if frames == 0 || position > math.MaxUint64-uint64(frames) {
		return 0, inputDiscontinuity{render.ErrDriver}
	}
	s.valid, s.endPosition, s.qpc = true, position+uint64(frames), qpc
	return int64(qpc) * 100, nil
}

func (p *driver) capturePackets(client unsafe.Pointer, capacity uint32, silence []float32, timeline *captureTimeline) error {
	// Limit each wake to one endpoint buffer, giving commands/shutdown priority
	// even if the device keeps producing while we drain it.
	for drained := uint32(0); drained < capacity; {
		var next uint32
		if err := waError(invoke(client, 5, uintptr(unsafe.Pointer(&next)))); err != nil {
			return err
		}
		if next == 0 {
			return nil
		}
		if next > capacity {
			return inputDiscontinuity{render.ErrDriver}
		}
		var data unsafe.Pointer
		var frames, flags uint32
		var position, qpc uint64
		hr := invoke(client, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&position)), uintptr(unsafe.Pointer(&qpc)))
		if err := waError(hr); err != nil {
			return err
		}
		if frames == 0 { // No acquired packet to release.
			if hr == captureBufferEmpty {
				return nil
			}
			return inputDiscontinuity{render.ErrDriver}
		}
		var packetErr error
		if hr != 0 || frames > capacity || frames != next || (data == nil && flags&bufferSilent == 0) {
			packetErr = inputDiscontinuity{render.ErrDriver}
		} else {
			var first int64
			first, packetErr = timeline.timestamp(frames, flags, position, qpc)
			if packetErr == nil && first > 0 {
				n := int(frames) * p.dev.Format().Channels
				if flags&bufferSilent != 0 {
					p.dev.Process(silence[:n], first)
				} else {
					p.dev.Process(unsafe.Slice((*float32)(data), n), first)
				}
			}
		}
		if err := errors.Join(packetErr, waError(invoke(client, 4, uintptr(frames)))); err != nil {
			return err
		}
		drained += frames
	}
	// Auto-reset events can coalesce while draining. Re-arm after a bounded batch
	// so an already queued next packet is not left waiting for a new native event.
	return windows.SetEvent(p.audioEvent)
}
