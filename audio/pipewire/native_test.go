//go:build linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"github.com/echocat/slf4g/testing/recording"
	"golang.org/x/sys/unix"
)

func TestNativeLoopMutexKeepsOSThreadOwnership(t *testing.T) {
	// Make the competing goroutine take over the current thread whenever the
	// caller is not pinned. No real PipeWire library, daemon or hardware is used.
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	for _, operation := range []string{"ready", "ready-error", "start", "start-error", "stop", "stop-error"} {
		t.Run(operation, func(t *testing.T) {
			for range 20 {
				var owner int
				release, joined := make(chan struct{}), make(chan struct{})
				checkOwner := func() {
					t.Helper()
					if tid := unix.Gettid(); tid != owner {
						t.Errorf("native loop mutex changed OS thread: locked on %d, used on %d", owner, tid)
					}
					runtime.Gosched()
				}
				p := &driver{api: &api{}}
				p.api.loopLock = func(uintptr) {
					owner = unix.Gettid()
					entered := make(chan struct{})
					go func() {
						runtime.LockOSThread()
						defer runtime.UnlockOSThread()
						defer close(joined)
						close(entered)
						<-release
					}()
					<-entered
				}
				p.api.loopUnlock = func(uintptr) {
					checkOwner()
					close(release)
					<-joined
				}
				code := int32(0)
				if operation == "ready-error" || operation == "start-error" || operation == "stop-error" {
					code = -int32(unix.EIO)
				}
				p.api.loopGetTime = func(uintptr, *unix.Timespec, int64) int32 { checkOwner(); return 0 }
				p.api.loopWait = func(uintptr, *unix.Timespec) int32 {
					checkOwner()
					p.state.Store(2)
					return code
				}
				p.api.streamActive = func(uintptr, bool) int32 { checkOwner(); return code }
				p.api.streamFlush = func(uintptr, bool) int32 { checkOwner(); return code }
				var err error
				switch operation {
				case "ready", "ready-error":
					err = p.waitReady()
				case "start", "start-error":
					err = p.Start()
				case "stop", "stop-error":
					err = p.Stop()
				}
				if (err != nil) != (code < 0) {
					t.Fatal("native operation lost its error", err)
				}
				// The competing goroutine was joined inside unlock, even on error.
			}
		})
	}
}

func TestNativeReadinessFailureUnlocksLoop(t *testing.T) {
	unlocked := false
	p := &driver{api: &api{
		loopLock:    func(uintptr) {},
		loopUnlock:  func(uintptr) { unlocked = true },
		loopGetTime: func(uintptr, *unix.Timespec, int64) int32 { return 0 },
	}}
	p.state.Store(-1)
	if err := p.waitReady(); !errors.Is(err, render.ErrDriver) || !unlocked {
		t.Fatal("failed stream retained its native loop mutex", err, unlocked)
	}
}

func TestNativeInlineCallbacksAndBackpressureCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The allocation is deliberately larger than Requested. Native frame demand
	// must remain authoritative, also during callbacks triggered by Stop/Flush.
	samples := make([]float32, 4096)
	chunk := spaChunk{}
	data := spaData{Data: unsafe.Pointer(&samples[0]), MaxSize: uint32(len(samples) * 4), Chunk: &chunk}
	buffer := spaBuffer{NDatas: 1, Datas: &data}
	native := pwBuffer{Buffer: &buffer, Requested: 480}
	var loop sync.Mutex
	var order []string
	p := &driver{loop: 1, stream: 1, api: &api{}}
	p.state.Store(2)
	p.api.loopLock = func(uintptr) { loop.Lock() }
	p.api.loopUnlock = func(uintptr) { loop.Unlock() }
	p.api.dequeue = func(uintptr) *pwBuffer { return &native }
	p.api.queue = func(uintptr, *pwBuffer) int32 { return 0 }
	p.api.time = func(_ uintptr, at *pwTime, _ uint64) int32 {
		*at = pwTime{Now: bridge.Now(), Rate: fraction{1, 48000}}
		return 0
	}
	p.api.streamActive = func(_ uintptr, active bool) int32 {
		if active {
			order = append(order, "start")
		} else {
			order = append(order, "stop")
		}
		// PipeWire may invoke Go before the native lifecycle call returns. This
		// must not encounter Device.mu held by its caller.
		p.process()
		return 0
	}
	p.api.streamFlush = func(uintptr, bool) int32 {
		order = append(order, "flush")
		p.process()
		return 0
	}
	p.api.loopStop = func(uintptr) { order = append(order, "join") }
	p.api.streamDestroy = func(uintptr) { order = append(order, "stream-destroy") }
	p.api.loopDestroy = func(uintptr) { order = append(order, "loop-destroy") }
	d, err := render.Open(ctx, render.Config{Backend: "fake PipeWire", BufferDuration: 10 * time.Millisecond, Logger: recording.NewLogger()}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *render.Device) (render.Driver, error) {
		p.dev = d
		return p, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err = d.Next(ctx); err != nil {
		t.Fatal("inline start callback failed", err)
	}
	writeCtx, stopWrite := context.WithCancel(ctx)
	defer stopWrite()
	written := make(chan error, 1)
	media := make([]float32, 1920)
	for i := range media {
		media[i] = .25
	}
	go func() { written <- d.Write(writeCtx, media) }()
	for p.dev.Diagnostics().QueuedFrames != 480 && ctx.Err() == nil {
		runtime.Gosched()
	}
	stopWrite()
	// Always join the writer; cancellation cannot be counted as success while
	// native backpressure still owns an operation or a blocked goroutine.
	if err = <-written; !errors.Is(err, context.Canceled) {
		t.Fatal("backpressured writer did not join on cancellation", err)
	}
	if err = d.Flush(ctx); err != nil {
		t.Fatal("inline stop/flush callback failed", err)
	}
	if native.Size != 480 || chunk.Size != 480*2*4 || !slices.Equal(samples, make([]float32, len(samples))) {
		t.Fatal("stopped callback changed quantum or released queued media", native.Size, chunk.Size)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"start", "stop", "flush", "join", "stream-destroy", "loop-destroy"}) {
		t.Fatal("native resources were destroyed before callback join", order)
	}
	callbacks := p.dev.Diagnostics().Callbacks
	p.process()
	if p.dev.Diagnostics().Callbacks != callbacks {
		t.Fatal("closed stream accepted another process callback")
	}
}
