//go:build linux && (amd64 || arm64)

package pipewire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	"github.com/echocat/sendspin-player-go/audio/internal/dynlib"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"golang.org/x/sys/unix"
)

type fraction struct{ Num, Denom uint32 }
type pwTime struct {
	Now              int64
	Rate             fraction
	Ticks            uint64
	Delay            int64
	Queued, Buffered uint64
	Reserved         [2]uint64
}
type pwBuffer struct {
	Buffer          *spaBuffer
	UserData        uintptr
	Size, Requested uint64
}
type spaBuffer struct {
	NMetas, NDatas uint32
	Metas          *spaMeta
	Datas          *spaData
}
type spaMeta struct {
	Type, Size uint32
	Data       unsafe.Pointer
}
type metaHeader struct {
	Flags, Offset  uint32
	PTS, DTSOffset int64
	Sequence       uint64
}
type spaData struct {
	Type, Flags        uint32
	FD                 int64
	MapOffset, MaxSize uint32
	Data               unsafe.Pointer
	Chunk              *spaChunk
}
type spaChunk struct {
	Offset, Size uint32
	Stride       int32
	Flags        uint32
}
type clockPrefix struct {
	Reserved           [80]byte
	Rate               fraction
	Position, Duration uint64
}
type rateMatchPrefix struct{ Delay, Size uint32 }
type pwEvents struct {
	Version   uint32
	Padding   uint32
	Callbacks [11]uintptr
}

type api struct {
	init                                        func(uintptr, uintptr)
	loopNew                                     func(string, uintptr) uintptr
	loopDestroy, loopStop, loopLock, loopUnlock func(uintptr)
	loopStart                                   func(uintptr) int32
	loopGetLoop                                 func(uintptr) uintptr
	loopSignal                                  func(uintptr, bool)
	loopGetTime                                 func(uintptr, *unix.Timespec, int64) int32
	loopWait                                    func(uintptr, *unix.Timespec) int32
	propsNew                                    func(uintptr) uintptr
	propsSet                                    func(uintptr, string, string) int32
	propsFree                                   func(uintptr)
	streamNew                                   func(uintptr, string, uintptr, *pwEvents, uintptr) uintptr
	streamDestroy                               func(uintptr)
	streamConnect                               func(uintptr, int32, uint32, uint32, **byte, uint32) int32
	streamActive                                func(uintptr, bool) int32
	streamFlush                                 func(uintptr, bool) int32
	dequeue                                     func(uintptr) *pwBuffer
	queue                                       func(uintptr, *pwBuffer) int32
	time                                        func(uintptr, *pwTime, uint64) int32
}

var libraries = struct {
	sync.Mutex
	values map[string]*api
}{values: make(map[string]*api)}

func loadAPI(path string) (*api, error) {
	lib, err := dynlib.Open(path, "libpipewire-0.3.so.0")
	if err != nil {
		return nil, err
	}
	libraries.Lock()
	defer libraries.Unlock()
	if a := libraries.values[lib.Path]; a != nil {
		return a, nil
	}
	a := &api{}
	bindings := []struct {
		dst  any
		name string
	}{
		{&a.init, "pw_init"}, {&a.loopNew, "pw_thread_loop_new"}, {&a.loopDestroy, "pw_thread_loop_destroy"}, {&a.loopStart, "pw_thread_loop_start"}, {&a.loopStop, "pw_thread_loop_stop"}, {&a.loopLock, "pw_thread_loop_lock"}, {&a.loopUnlock, "pw_thread_loop_unlock"}, {&a.loopGetLoop, "pw_thread_loop_get_loop"}, {&a.loopSignal, "pw_thread_loop_signal"}, {&a.loopGetTime, "pw_thread_loop_get_time"}, {&a.loopWait, "pw_thread_loop_timed_wait_full"},
		{&a.propsNew, "pw_properties_new"}, {&a.propsSet, "pw_properties_set"}, {&a.propsFree, "pw_properties_free"}, {&a.streamNew, "pw_stream_new_simple"}, {&a.streamDestroy, "pw_stream_destroy"}, {&a.streamConnect, "pw_stream_connect"}, {&a.streamActive, "pw_stream_set_active"}, {&a.streamFlush, "pw_stream_flush"}, {&a.dequeue, "pw_stream_dequeue_buffer"}, {&a.queue, "pw_stream_queue_buffer"}, {&a.time, "pw_stream_get_time_n"},
	}
	for _, b := range bindings {
		if err = lib.Bind(b.dst, b.name); err != nil {
			return nil, err
		}
	}
	a.init(0, 0)
	libraries.values[lib.Path] = a
	return a, nil
}

type driver struct {
	api                 *api
	loop, stream, token uintptr
	dev                 *render.Device
	input               *capture.Device
	scratch, silence    []float32
	seen                bool
	state               atomic.Int32
	closing             atomic.Bool
	position            *clockPrefix
	match               *rateMatchPrefix
}

var callbacks struct {
	once   sync.Once
	events pwEvents
}

func events() *pwEvents {
	callbacks.once.Do(func() {
		callbacks.events.Version = 2
		callbacks.events.Callbacks[1] = purego.NewCallback(func(id uintptr, old, state int32, _ *byte) {
			p, ok := bridge.Value(id).(*driver)
			if !ok {
				return
			}
			p.state.Store(state)
			p.api.loopSignal(p.loop, false)
			if !p.closing.Load() && (state == -1 || (state == 0 && old != 0)) {
				p.fail(render.ErrDriver)
			}
		})
		callbacks.events.Callbacks[3] = purego.NewCallback(func(id uintptr, kind uint32, area unsafe.Pointer, size uint32) {
			p, ok := bridge.Value(id).(*driver)
			if !ok {
				return
			}
			if kind == 7 {
				p.position = nil
				if area != nil && size >= uint32(unsafe.Sizeof(clockPrefix{})) {
					p.position = (*clockPrefix)(area)
				}
			}
			if kind == 8 {
				p.match = nil
				if area != nil && size >= uint32(unsafe.Sizeof(rateMatchPrefix{})) {
					p.match = (*rateMatchPrefix)(area)
				}
			}
		})
		callbacks.events.Callbacks[7] = purego.NewCallback(func(id uintptr) {
			if p, ok := bridge.Value(id).(*driver); ok {
				p.process()
			}
		})
	})
	return &callbacks.events
}
func pwError(op string, code int32) error {
	if code < 0 {
		return fmt.Errorf("pipewire: %s failed (%d)", op, code)
	}
	return nil
}

// rawFormat builds the stable SPA Object/Format POD ABI. Only the negotiated
// sample rate/channels vary. Omitted positions mean unpositioned channels.
func rawFormat(rate, channels int) []byte {
	b := make([]byte, 136)
	put := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	put(0, 128)
	put(4, 15)
	put(8, 0x40003)
	put(12, 3)
	for i, p := range [][3]uint32{{1, 3, 1}, {2, 3, 1}, {0x10001, 3, 0x11b}, {0x10003, 4, uint32(rate)}, {0x10004, 4, uint32(channels)}} {
		at := 16 + i*24
		put(at, p[0])
		put(at+8, 4)
		put(at+12, p[1])
		put(at+16, p[2])
	}
	return b
}
func openNative(o Output, d *render.Device) (render.Driver, error) {
	return openStream(o.Device, o.SharedLibPath, d, nil, false)
}
func openStream(device, path string, out *render.Device, in *capture.Device, loopback bool) (render.Driver, error) {
	a, err := loadAPI(path)
	if err != nil {
		return nil, err
	}
	if bridge.Now() <= 0 {
		return nil, render.ErrTiming
	}
	p := &driver{api: a, dev: out, input: in}
	category, nodeName := "Playback", "sendspin-player"
	direction := int32(1)
	var rate, channels int
	if in != nil {
		category, nodeName, direction = "Capture", "sendspin-source", 0
		rate, channels = in.Format().SampleRate, in.Format().Channels
		p.scratch = make([]float32, in.MaxSamples())
		p.silence = make([]float32, in.MaxSamples())
	} else {
		rate, channels = out.Format().SampleRate, out.Format().Channels
	}
	p.token = bridge.Register(p)
	fail := func(err error) (render.Driver, error) { _ = p.Close(); return nil, err }
	p.loop = a.loopNew("sendspin-playback", 0)
	if p.loop == 0 {
		return fail(render.ErrDriver)
	}
	props := a.propsNew(0)
	if props == 0 {
		return fail(render.ErrDriver)
	}
	for _, property := range [][2]string{{"media.type", "Audio"}, {"media.category", category}, {"media.role", "Music"}, {"node.name", nodeName}, {"node.dont-reconnect", "true"}} {
		if code := a.propsSet(props, property[0], property[1]); code < 0 {
			a.propsFree(props)
			return fail(pwError("properties", code))
		}
	}
	if device != "" {
		if code := a.propsSet(props, "target.object", device); code < 0 {
			a.propsFree(props)
			return fail(pwError("target", code))
		}
	}
	if loopback {
		if code := a.propsSet(props, "stream.capture.sink", "true"); code < 0 {
			a.propsFree(props)
			return fail(pwError("capture sink", code))
		}
	}
	p.stream = a.streamNew(a.loopGetLoop(p.loop), "Sendspin playback", props, events(), p.token)
	if p.stream == 0 {
		return fail(render.ErrDriver)
	}
	pod := rawFormat(rate, channels)
	meta := headerFormat()
	params := [2]*byte{&pod[0], &meta[0]}
	count := uint32(1)
	if in != nil {
		count = 2
	}
	code := a.streamConnect(p.stream, direction, ^uint32(0), 7, &params[0], count)
	runtime.KeepAlive(pod)
	runtime.KeepAlive(meta)
	if code < 0 {
		return fail(pwError("connect", code))
	}
	if code = a.loopStart(p.loop); code < 0 {
		return fail(pwError("loop start", code))
	}
	if err := p.waitReady(); err != nil {
		return fail(err)
	}
	return p, nil
}

// PipeWire's recursive pthread mutex belongs to an OS thread, not a Go
// goroutine. Keep its owner pinned across every native call (including waits)
// until the mutex has actually been released.
func (p *driver) lockLoop() {
	runtime.LockOSThread()
	p.api.loopLock(p.loop)
}
func (p *driver) unlockLoop() {
	p.api.loopUnlock(p.loop)
	runtime.UnlockOSThread()
}
func (p *driver) waitReady() error {
	p.lockLoop()
	defer p.unlockLoop()
	var deadline unix.Timespec
	code := p.api.loopGetTime(p.loop, &deadline, 2_000_000_000)
	for code >= 0 && p.state.Load() != 2 && p.state.Load() != 3 && p.state.Load() != -1 {
		code = p.api.loopWait(p.loop, &deadline)
	}
	state := p.state.Load()
	if code < 0 {
		return pwError("stream readiness", code)
	}
	if state == -1 {
		return render.ErrDriver
	}
	return nil
}
func (p *driver) process() {
	if p.closing.Load() {
		return
	}
	if p.input != nil {
		p.processInput()
		return
	}
	b := p.api.dequeue(p.stream)
	if b == nil {
		return
	}
	queue := func() {
		if p.api.queue(p.stream, b) < 0 {
			p.dev.Fail(render.ErrDriver)
		}
	}
	if b.Buffer == nil || b.Buffer.NDatas != 1 || b.Buffer.Datas == nil {
		p.dev.Fail(render.ErrDriver)
		queue()
		return
	}
	data := b.Buffer.Datas
	if data.Data == nil || data.Chunk == nil {
		p.dev.Fail(render.ErrDriver)
		queue()
		return
	}
	stride := uint32(p.dev.Format().Channels * 4)
	var matched uint32
	var duration uint64
	var num, denom uint32
	if p.match != nil {
		matched = p.match.Size
	}
	if p.position != nil {
		duration, num, denom = p.position.Duration, p.position.Rate.Num, p.position.Rate.Denom
	}
	frames := bridge.PWFrames(b.Requested, matched, duration, num, denom, uint32(p.dev.Format().SampleRate), data.MaxSize/stride)
	if frames == 0 || frames > uint32(p.dev.Format().SampleRate/2) {
		data.Chunk.Size = 0
		b.Size = 0
		p.dev.Fail(render.ErrDriver)
		queue()
		return
	}
	var t pwTime
	var first int64
	if p.api.time(p.stream, &t, uint64(unsafe.Sizeof(t))) == 0 {
		first = bridge.PWTime(t.Now, t.Delay, t.Rate.Num, t.Rate.Denom, t.Queued, t.Buffered, uint32(p.dev.Format().SampleRate))
	}
	p.dev.Process(unsafe.Slice((*float32)(data.Data), int(frames)*p.dev.Format().Channels), first)
	data.Chunk.Offset = 0
	data.Chunk.Stride = int32(stride)
	data.Chunk.Size = frames * stride
	b.Size = uint64(frames)
	queue()
}
func (p *driver) fail(err error) {
	if p.input != nil {
		p.input.Fail(err)
	} else if p.dev != nil {
		p.dev.Fail(err)
	}
}
func (p *driver) Start() error {
	p.lockLoop()
	defer p.unlockLoop()
	code := p.api.streamActive(p.stream, true)
	return pwError("start", code)
}
func (p *driver) Stop() error {
	p.lockLoop()
	defer p.unlockLoop()
	a := p.api.streamActive(p.stream, false)
	b := p.api.streamFlush(p.stream, false)
	return errors.Join(pwError("stop", a), pwError("flush", b))
}
func (p *driver) Check() error {
	if state := p.state.Load(); state == -1 || state == 0 {
		return render.ErrDriver
	}
	return nil
}
func (p *driver) Clock() int64 { return bridge.Now() }
func (p *driver) Close() error {
	p.closing.Store(true)
	if p.loop != 0 {
		p.api.loopStop(p.loop)
	}
	if p.stream != 0 {
		p.api.streamDestroy(p.stream)
		p.stream = 0
	}
	if p.loop != 0 {
		p.api.loopDestroy(p.loop)
		p.loop = 0
	}
	bridge.Free(p.token)
	return nil
}
