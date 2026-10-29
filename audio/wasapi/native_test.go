//go:build windows && (amd64 || arm64)

package wasapi

// All COM objects in this file are synthetic vtables. No test opens a Windows
// audio endpoint, starts real capture/render, or changes a host mixer/default.
import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	"golang.org/x/sys/windows"
)

type fakeCOM struct {
	comObject
	table [15]uintptr
}

func newFakeCOM(methods map[int]any) *fakeCOM {
	f := &fakeCOM{}
	f.Methods = &f.table
	for index, method := range methods {
		f.table[index] = syscall.NewCallback(method)
	}
	return f
}

func TestWASAPIInvokeABI(t *testing.T) {
	f := newFakeCOM(map[int]any{3: func(this uintptr, out *uint64, marker uintptr) uintptr {
		if this == 0 || marker != 0xaabbccdd {
			return 0x80004003
		}
		*out = 0x0123456789abcdef
		return 0x80004005
	}})
	var out uint64
	hr := invoke(unsafe.Pointer(f), 3, uintptr(unsafe.Pointer(&out)), 0xaabbccdd)
	if out != 0x0123456789abcdef || hr != -2147467259 || waError(hr) == nil || waError(1) != nil {
		t.Fatalf("HRESULT/pointer ABI mismatch: %x, %d", out, hr)
	}
	for _, index := range []int{-1, 15, 0} {
		if invoke(unsafe.Pointer(f), index) >= 0 {
			t.Fatal("invalid vtable access accepted", index)
		}
	}
}

func TestWASAPICaptureTimeline(t *testing.T) {
	var s captureTimeline
	if first, err := s.timestamp(4, 0, 0, 0); first != 0 || err != nil || s.valid {
		t.Fatal("startup zero QPC should not create a timestamp", first, err)
	}
	if first, err := s.timestamp(4, 0, 0, 100); first != 10000 || err != nil {
		t.Fatal("QPC must be in 100ns units, device position zero is valid", first, err)
	}
	if _, err := s.timestamp(4, bufferSilent, 4, 101); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		frames, flags uint32
		position, qpc uint64
	}{
		{"discontinuity", 4, bufferDiscontinuity, 8, 102},
		{"timestamp-error", 4, bufferTimestampError, 8, 102},
		{"unknown-flag", 4, 8, 8, 102},
		{"lost-frames", 4, 0, 9, 102},
		{"backwards-position", 4, 0, 0, 102},
		{"backwards-qpc", 4, 0, 8, 100},
		{"zero-after-start", 4, 0, 8, 0},
		{"qpc-overflow", 4, 0, 8, math.MaxInt64/100 + 1},
		{"zero-frames", 0, 0, 8, 102},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := s
			_, err := copy.timestamp(tc.frames, tc.flags, tc.position, tc.qpc)
			var discontinuity audio.InputDiscontinuityError
			if !errors.As(err, &discontinuity) || !discontinuity.AudioInputDiscontinuity() {
				t.Fatal("unreported input discontinuity", err)
			}
		})
	}
	var fresh captureTimeline
	if _, err := fresh.timestamp(4, 0, math.MaxUint64-2, 100); err == nil {
		t.Fatal("device frame position overflow accepted")
	}
}

type observedBlock struct {
	samples []float32
	first   int64
}
type observedDevice struct {
	blocks chan observedBlock
	fault  chan error
	render bool
}

func (d *observedDevice) Format() audio.Format { return audio.Format{SampleRate: 48000, Channels: 2} }
func (d *observedDevice) MaxSamples() int      { return 48000 }
func (d *observedDevice) Process(samples []float32, first int64) {
	select {
	case d.blocks <- observedBlock{append([]float32(nil), samples...), first}:
	default:
	}
	// Render buffers are filled with a recognizable, nonzero sample to prove
	// that missing startup timing never invokes Process.
	if d.render {
		for i := range samples {
			samples[i] = .625
		}
	}
}
func (d *observedDevice) Fail(err error) {
	select {
	case d.fault <- err:
	default:
	}
}

type fakePacket struct {
	samples       []float32
	frames, flags uint32
	position, qpc uint64
}
type fakeClock struct {
	position, qpc uint64
	hr            uintptr
}

type workerFixture struct {
	t                       *testing.T
	p                       *driver
	thread                  atomic.Uint32
	startErr                uintptr
	violations              chan string
	released                chan string
	rendered                chan observedBlock
	mu                      sync.Mutex
	packets                 []fakePacket
	clocks                  []fakeClock
	renderBuffer            [8]float32
	started, stopped, reset atomic.Int32
	packetReleases          atomic.Int32
	objects                 []*fakeCOM
}

func (f *workerFixture) check(ok bool, message string) {
	if !ok {
		select {
		case f.violations <- message:
		default:
		}
	}
}
func (f *workerFixture) checkThread() {
	f.check(windows.GetCurrentThreadId() == f.thread.Load(), "COM method ran outside owning thread")
}
func (f *workerFixture) object(name string, methods map[int]any) *fakeCOM {
	methods[2] = func(this uintptr) uintptr {
		f.checkThread()
		f.released <- name
		return 0
	}
	o := newFakeCOM(methods)
	f.objects = append(f.objects, o)
	return o
}

func newWorkerFixture(t *testing.T, isCapture, loopback bool, endpoint string, dev nativeDevice) *workerFixture {
	t.Helper()
	f := &workerFixture{t: t, violations: make(chan string, 32), released: make(chan string, 16), rendered: make(chan observedBlock, 16)}
	p := &driver{dev: dev, capture: isCapture, loopback: loopback, commands: make(chan command, 1), ready: make(chan error, 1), done: make(chan struct{}), shutdown: make(chan struct{})}
	f.p = p
	var err error
	p.format, err = waveFormat(dev.Format().SampleRate, dev.Format().Channels)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "" {
		p.endpoint, err = windows.UTF16PtrFromString(endpoint)
		if err != nil {
			t.Fatal(err)
		}
	}
	p.commandEvent, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.audioEvent, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(p.commandEvent)
		t.Fatal(err)
	}
	capturer := f.object("capture", map[int]any{
		3: func(this uintptr, data *unsafe.Pointer, frames, flags *uint32, position, qpc *uint64) uintptr {
			f.checkThread()
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) == 0 {
				*frames = 0
				return 0x08890001
			}
			packet := &f.packets[0]
			*data = nil
			if len(packet.samples) > 0 {
				*data = unsafe.Pointer(&packet.samples[0])
			}
			*frames = packet.frames
			*flags = packet.flags
			*position = packet.position
			*qpc = packet.qpc
			return 0
		},
		4: func(this, frames uintptr) uintptr {
			f.checkThread()
			f.mu.Lock()
			defer f.mu.Unlock()
			f.check(len(f.packets) > 0 && uint32(frames) == f.packets[0].frames, "capture ReleaseBuffer frame count")
			f.packetReleases.Add(1)
			if len(f.packets) > 0 {
				f.packets = f.packets[1:]
			}
			return 0
		},
		5: func(this uintptr, frames *uint32) uintptr {
			f.checkThread()
			f.mu.Lock()
			defer f.mu.Unlock()
			*frames = 0
			if len(f.packets) > 0 {
				*frames = f.packets[0].frames
			}
			return 0
		},
	})
	renderer := f.object("render", map[int]any{
		3: func(this, frames uintptr, data *unsafe.Pointer) uintptr {
			f.checkThread()
			f.check(frames <= 4, "render GetBuffer bound")
			*data = unsafe.Pointer(&f.renderBuffer[0])
			return 0
		},
		4: func(this, frames, flags uintptr) uintptr {
			f.checkThread()
			b := observedBlock{samples: append([]float32(nil), f.renderBuffer[:int(frames)*2]...), first: int64(flags)}
			select {
			case f.rendered <- b:
			default:
			}
			return 0
		},
	})
	clock := f.object("clock", map[int]any{
		3: func(this uintptr, frequency *uint64) uintptr {
			f.checkThread()
			*frequency = 48000
			return 0
		},
		4: func(this uintptr, position, qpc *uint64) uintptr {
			f.checkThread()
			f.mu.Lock()
			defer f.mu.Unlock()
			value := fakeClock{hr: 1}
			if len(f.clocks) > 0 {
				value = f.clocks[0]
				f.clocks = f.clocks[1:]
			}
			*position = value.position
			*qpc = value.qpc
			return value.hr
		},
	})
	client := f.object("client", map[int]any{
		3: func(this, share, flags, duration, period uintptr, format *[40]byte, session uintptr) uintptr {
			f.checkThread()
			wantFlags := uintptr(streamEventCallback | streamAutoConvert | streamSRCQuality | streamNoPersist)
			if isCapture && loopback {
				wantFlags |= streamLoopback
			}
			f.check(share == 0 && flags == wantFlags && duration == 500000 && period == 0 && session == 0, "IAudioClient Initialize shared flags/ABI")
			f.check(*format == p.format, "IAudioClient WAVEFORMATEXTENSIBLE packing")
			return 0
		},
		4: func(this uintptr, capacity *uint32) uintptr {
			f.checkThread()
			*capacity = 4
			return 0
		},
		6: func(this uintptr, padding *uint32) uintptr {
			f.checkThread()
			*padding = 0
			return 0
		},
		10: func(this uintptr) uintptr { f.checkThread(); f.started.Add(1); return f.startErr },
		11: func(this uintptr) uintptr { f.checkThread(); f.stopped.Add(1); return 0 },
		12: func(this uintptr) uintptr { f.checkThread(); f.reset.Add(1); return 0 },
		13: func(this, event uintptr) uintptr {
			f.checkThread()
			f.check(windows.Handle(event) == p.audioEvent, "SetEventHandle index")
			return 0
		},
		14: func(this uintptr, iid *windows.GUID, out *unsafe.Pointer) uintptr {
			f.checkThread()
			requested := *iid
			var object *fakeCOM
			switch requested {
			case captureIID:
				object = capturer
			case renderIID:
				object = renderer
			case clockIID:
				object = clock
			default:
				f.check(false, "unknown service IID")
				return 0x80004002
			}
			*out = unsafe.Pointer(object)
			return 0
		},
	})
	device := f.object("device", map[int]any{
		3: func(this uintptr, iid *windows.GUID, clsctx, activation uintptr, out *unsafe.Pointer) uintptr {
			f.checkThread()
			f.check(*iid == clientIID && clsctx == 23 && activation == 0, "IMMDevice Activate ABI")
			*out = unsafe.Pointer(client)
			return 0
		},
	})
	enumerator := f.object("enumerator", map[int]any{
		4: func(this, flow, role uintptr, out *unsafe.Pointer) uintptr {
			f.checkThread()
			wantFlow := uintptr(0)
			if isCapture && !loopback {
				wantFlow = 1
			}
			f.check(endpoint == "" && flow == wantFlow && role == 1, "default endpoint direction/role")
			*out = unsafe.Pointer(device)
			return 0
		},
		5: func(this uintptr, id *uint16, out *unsafe.Pointer) uintptr {
			f.checkThread()
			f.check(endpoint != "" && windows.UTF16PtrToString(id) == endpoint, "explicit endpoint ID")
			*out = unsafe.Pointer(device)
			return 0
		},
	})
	p.api = &api{
		initialize: func(reserved uintptr, mode uint32) int32 {
			f.thread.Store(windows.GetCurrentThreadId())
			f.check(reserved == 0 && mode == 0, "CoInitializeEx MTA ABI")
			return 1 // S_FALSE still requires Uninitialize.
		},
		create: func(clsid *windows.GUID, outer uintptr, clsctx uint32, iid *windows.GUID, out *unsafe.Pointer) int32 {
			f.checkThread()
			f.check(*clsid == enumeratorID && outer == 0 && clsctx == 23 && *iid == enumeratorIID, "CoCreateInstance ABI")
			*out = unsafe.Pointer(enumerator)
			return 0
		},
		uninitialize: func() { f.checkThread(); f.released <- "uninitialize" },
	}
	t.Cleanup(func() {
		_ = p.Close()
		for {
			select {
			case violation := <-f.violations:
				t.Error(violation)
			default:
				return
			}
		}
	})
	return f
}

func (f *workerFixture) launch() error {
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := f.p.worker()
		f.p.fail(err)
		f.p.signalReady(err)
		close(f.p.done)
	}()
	select {
	case err := <-f.p.ready:
		return err
	case <-time.After(3 * time.Second):
		f.t.Fatal("worker did not become ready")
		return nil
	}
}
func (f *workerFixture) push(packets ...fakePacket) {
	f.mu.Lock()
	f.packets = append(f.packets, packets...)
	f.mu.Unlock()
	if err := windows.SetEvent(f.p.audioEvent); err != nil {
		f.t.Fatal(err)
	}
}
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for synthetic worker")
		var zero T
		return zero
	}
}

func TestWASAPIWorkerCaptureEndpointLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		loopback       bool
	}{
		{"default-capture", "", false}, {"explicit-capture", "capture-endpoint-id", false}, {"default-explicit-loopback", "", true}, {"explicit-loopback", "render-endpoint-id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dev := &observedDevice{blocks: make(chan observedBlock, 8), fault: make(chan error, 8)}
			f := newWorkerFixture(t, true, tc.loopback, tc.endpoint, dev)
			if err := f.launch(); err != nil {
				t.Fatal(err)
			}
			if f.started.Load() != 0 {
				t.Fatal("Open activated capture")
			}
			if err := f.p.Start(); err != nil {
				t.Fatal(err)
			}
			if err := f.p.Stop(); err != nil {
				t.Fatal(err)
			}
			if err := f.p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.p.Close(); err != nil {
				t.Fatal(err)
			}
			if f.started.Load() != 1 || f.stopped.Load() != 1 || f.reset.Load() != 2 {
				t.Fatal("incorrect Start/Stop/Reset counts")
			}
			var released []string
			for len(f.released) > 0 {
				released = append(released, <-f.released)
			}
			if want := []string{"capture", "client", "device", "enumerator", "uninitialize"}; !reflect.DeepEqual(released, want) {
				t.Fatalf("release order: %v != %v", released, want)
			}
			if !errors.Is(f.p.Start(), render.ErrClosed) {
				t.Fatal("Start after Close did not fail")
			}
		})
	}
}

func TestWASAPIWorkerCommandAndOpenErrors(t *testing.T) {
	for _, when := range []string{"initialize", "create", "start"} {
		t.Run(when, func(t *testing.T) {
			dev := &observedDevice{blocks: make(chan observedBlock, 8), fault: make(chan error, 8)}
			f := newWorkerFixture(t, true, false, "", dev)
			switch when {
			case "initialize":
				f.p.api.initialize = func(uintptr, uint32) int32 { return -2147467259 }
			case "create":
				f.p.api.create = func(*windows.GUID, uintptr, uint32, *windows.GUID, *unsafe.Pointer) int32 { return -2147467259 }
			case "start":
				f.startErr = 0x80004005
			}
			err := f.launch()
			if when == "start" {
				if err != nil {
					t.Fatal(err)
				}
				err = f.p.Start()
			}
			if err == nil {
				t.Fatal("native error was not returned")
			}
			if err := f.p.Close(); err == nil {
				t.Fatal("Close lost worker failure")
			}
		})
	}
}

func TestWASAPIWorkerConcurrentCommandsAndClose(t *testing.T) {
	dev := &observedDevice{blocks: make(chan observedBlock, 8), fault: make(chan error, 8)}
	f := newWorkerFixture(t, true, false, "", dev)
	if err := f.launch(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 17)
	for i := range 16 {
		go func() {
			if i%2 == 0 {
				result <- f.p.Start()
			} else {
				result <- f.p.Stop()
			}
		}()
	}
	go func() { result <- f.p.Close() }()
	for range 17 {
		if err := await(t, result); err != nil && !errors.Is(err, render.ErrClosed) {
			t.Fatal("concurrent command lost lifecycle synchronization", err)
		}
	}
	select {
	case <-f.p.done:
	default:
		t.Fatal("Close did not join worker")
	}
}

func TestWASAPIWorkerRenderStartupTiming(t *testing.T) {
	dev := &observedDevice{blocks: make(chan observedBlock, 8), fault: make(chan error, 8), render: true}
	f := newWorkerFixture(t, false, false, "", dev)
	if err := f.launch(); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Start(); err != nil {
		t.Fatal(err)
	}
	if got := await(t, f.rendered); got.first != bufferSilent {
		t.Fatal("initial render buffer was not silent")
	}
	for _, clock := range []fakeClock{{position: 1, qpc: uint64(bridge.Now() / 100), hr: 1}, {position: 0, qpc: uint64(bridge.Now() / 100)}, {position: 1, qpc: 0}} {
		f.mu.Lock()
		f.clocks = append(f.clocks, clock)
		f.mu.Unlock()
		if err := windows.SetEvent(f.p.audioEvent); err != nil {
			t.Fatal(err)
		}
		if got := await(t, f.rendered); got.first != bufferSilent {
			t.Fatal("invalid startup timing released amplitudes")
		}
		select {
		case <-dev.blocks:
			t.Fatal("Process called with missing startup timestamp")
		default:
		}
	}
	f.mu.Lock()
	f.clocks = append(f.clocks, fakeClock{position: 4, qpc: uint64(bridge.Now() / 100)})
	f.mu.Unlock()
	if err := windows.SetEvent(f.p.audioEvent); err != nil {
		t.Fatal(err)
	}
	if got := await(t, f.rendered); got.first != 0 || got.samples[0] != .625 {
		t.Fatal("valid render timing failed", got)
	}
	if got := await(t, dev.blocks); got.first <= 0 {
		t.Fatal("valid output timestamp missing")
	}
	f.mu.Lock()
	f.clocks = append(f.clocks, fakeClock{position: 0, qpc: uint64(bridge.Now() / 100)})
	f.mu.Unlock()
	if err := windows.SetEvent(f.p.audioEvent); err != nil {
		t.Fatal(err)
	}
	if err := await(t, dev.fault); !errors.Is(err, render.ErrTiming) {
		t.Fatal("clock loss after startup was hidden", err)
	}
	if err := f.p.Close(); !errors.Is(err, render.ErrTiming) {
		t.Fatal("Close lost output timing error", err)
	}
}

func TestWASAPIWorkerCapturePackets(t *testing.T) {
	dev := &observedDevice{blocks: make(chan observedBlock, 8), fault: make(chan error, 8)}
	f := newWorkerFixture(t, true, false, "", dev)
	if err := f.launch(); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Start(); err != nil {
		t.Fatal(err)
	}
	// The first zero-timestamp packet is released but never presented as data.
	f.push(fakePacket{samples: []float32{.9, .8, .7, .6}, frames: 2})
	deadline := time.Now().Add(3 * time.Second)
	for f.packetReleases.Load() != 1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if f.packetReleases.Load() != 1 {
		t.Fatal("zero timestamp packet not released")
	}
	select {
	case <-dev.blocks:
		t.Fatal("zero timestamp emitted capture audio")
	default:
	}
	samples := []float32{.001, -.25, .75, -1}
	f.push(fakePacket{samples: samples, frames: 2, position: 2, qpc: 123456})
	got := await(t, dev.blocks)
	if !reflect.DeepEqual(got.samples, []float32{.001, -.25, .75, -1}) || got.first != 12345600 {
		t.Fatal("capture data/QPC was changed", got)
	}
	if !reflect.DeepEqual(samples, got.samples) {
		t.Fatal("capture modified native input")
	}
	f.push(fakePacket{frames: 2, flags: bufferSilent, position: 4, qpc: 123457})
	got = await(t, dev.blocks)
	if !reflect.DeepEqual(got.samples, []float32{0, 0, 0, 0}) {
		t.Fatal("silent packet was not zero", got)
	}
	f.push(fakePacket{samples: []float32{0, 0, 0, 0}, frames: 2, flags: bufferDiscontinuity, position: 6, qpc: 123458})
	err := await(t, dev.fault)
	var discontinuity audio.InputDiscontinuityError
	if !errors.As(err, &discontinuity) {
		t.Fatal("capture discontinuity not reported", err)
	}
	if err := f.p.Close(); err == nil {
		t.Fatal("Close lost capture failure")
	}
	if f.packetReleases.Load() != 4 {
		t.Fatal("capture buffers leaked", f.packetReleases.Load())
	}
}

func TestWASAPICaptureQueueGainTimestampAndClose(t *testing.T) {
	var f *workerFixture
	d, err := capture.Open(context.Background(), capture.Config{Backend: "wasapi"}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *capture.Device) (capture.Driver, error) {
		f = newWorkerFixture(t, true, false, "", d)
		if err := f.launch(); err != nil {
			return nil, err
		}
		return f.p, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	dst := make([]float32, 4)
	go func() {
		n, first, err := d.Read(ctx, dst)
		age := monotime.Since(first)
		if err == nil && (n != 4 || first.IsZero() || age < 0 || age > time.Second || first == first.Round(0)) {
			err = fmt.Errorf("invalid count/monotonic capture time: %d, %v", n, first)
		}
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for f.started.Load() == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if f.started.Load() == 0 {
		t.Fatal("Read did not start capture")
	}
	samples := []float32{.0001, -.125, .5, -1}
	f.push(fakePacket{samples: samples, frames: 2, qpc: uint64((bridge.Now() - int64(time.Millisecond)) / 100)})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
	if want := []float32{.0001, -.125, .5, -1}; !reflect.DeepEqual(dst, want) || !reflect.DeepEqual(samples, want) {
		t.Fatal("input amplitudes changed", dst, samples)
	}
	// Read cancellation and Close must wake blocked readers without waiting for
	// an endpoint event and leave the synthetic COM thread joined.
	readCtx, readCancel := context.WithCancel(context.Background())
	go func() { _, _, err := d.Read(readCtx, dst); result <- err }()
	readCancel()
	if err := await(t, result); !errors.Is(err, context.Canceled) {
		t.Fatal("Read cancellation failed", err)
	}
	go func() { _, _, err := d.Read(context.Background(), dst); result <- err }()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := await(t, result); !errors.Is(err, capture.ErrClosed) {
		t.Fatal("Close did not unblock Read", err)
	}
	select {
	case <-f.p.done:
	default:
		t.Fatal("Close did not join COM worker")
	}
}

func TestWASAPICaptureQueueRejectsNonfinite(t *testing.T) {
	for _, sample := range []float32{float32(math.NaN()), float32(math.Inf(1))} {
		t.Run(fmt.Sprint(sample), func(t *testing.T) {
			var f *workerFixture
			d, err := capture.Open(context.Background(), capture.Config{Backend: "wasapi"}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *capture.Device) (capture.Driver, error) {
				f = newWorkerFixture(t, true, false, "", d)
				if err := f.launch(); err != nil {
					return nil, err
				}
				return f.p, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			result := make(chan error, 1)
			go func() { _, _, err := d.Read(context.Background(), make([]float32, 2)); result <- err }()
			deadline := time.Now().Add(3 * time.Second)
			for f.started.Load() == 0 && time.Now().Before(deadline) {
				runtime.Gosched()
			}
			f.push(fakePacket{samples: []float32{sample, 0}, frames: 1, qpc: uint64(bridge.Now() / 100)})
			if err := await(t, result); !errors.Is(err, capture.ErrDriver) {
				t.Fatal("nonfinite capture accepted", err)
			}
		})
	}
}
