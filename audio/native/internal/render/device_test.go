package render

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/slf4g/level"
	"github.com/echocat/slf4g/testing/recording"
)

type fakeDriver struct {
	d                         *Device
	start, stop, close, check error
	starts, stops, closes     int
	checks, failAfter         int
	silent                    bool
	clock                     int64
}

func (f *fakeDriver) Clock() int64 { return f.clock }
func (f *fakeDriver) Start() error {
	f.starts++
	if f.start != nil {
		return f.start
	}
	if !f.silent {
		f.d.Process(make([]float32, 96), f.clock+int64(time.Millisecond))
	}
	return nil
}
func (f *fakeDriver) Stop() error  { f.stops++; return f.stop }
func (f *fakeDriver) Close() error { f.closes++; return f.close }
func (f *fakeDriver) Check() error {
	f.checks++
	if f.failAfter > 0 && f.checks >= f.failAfter {
		return ErrDriver
	}
	return f.check
}
func openFake(t *testing.T, f *fakeDriver) *Device {
	t.Helper()
	f.clock = int64(time.Second)
	device, err := Open(context.Background(), Config{Backend: "fake", BufferDuration: 10 * time.Millisecond, StartupTimeout: 20 * time.Millisecond, Logger: recording.NewLogger()}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *Device) (Driver, error) { f.d = d; return f, nil })
	if err != nil {
		t.Fatal(err)
	}
	d := device.(*Device)
	t.Cleanup(func() { _ = d.Close() })
	return d
}
func TestNativeFeedbackOwnershipAndBoundedQueue(t *testing.T) {
	f := &fakeDriver{}
	d := openFake(t, f)
	if f.starts != 0 {
		t.Fatal("Open started playback")
	}
	base := time.Now()
	d.now = func() time.Time { return base }
	next, err := d.Next(context.Background())
	if err != nil || next.Sub(base) != 2*time.Millisecond {
		t.Fatal(next.Sub(base), err)
	}
	samples := []float32{.25, -.5, 2, -2}
	if err = d.Write(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	clear(samples)
	out := make([]float32, 96)
	d.Process(out, f.clock+int64(2*time.Millisecond))
	if !slices.Equal(out[:4], []float32{.25, -.5, 1, -1}) {
		t.Fatal("ownership/conversion", out[:4])
	}
	if !slices.Equal(out[4:], make([]float32, 92)) {
		t.Fatal("underrun did not emit silence")
	}
	next, err = d.Next(context.Background())
	if err != nil || next.Sub(base) != 3*time.Millisecond {
		t.Fatal("native time was not refreshed", next.Sub(base), err)
	}
}
func TestReservedTimelineCannotMoveSilently(t *testing.T) {
	f := &fakeDriver{}
	d := openFake(t, f)
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.Process(make([]float32, 96), f.clock+int64(2*time.Millisecond))
	if err := d.Write(context.Background(), make([]float32, 96)); !errors.Is(err, ErrClockReset) {
		t.Fatal(err)
	}
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Write(context.Background(), make([]float32, 96)); err != nil {
		t.Fatal(err)
	}
}
func TestTimingFaultsNeverReleaseMedia(t *testing.T) {
	for _, name := range []string{"zero", "too-far", "too-old", "no-clock", "slow", "backwards", "invalid-frame"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDriver{}
			d := openFake(t, f)
			if _, err := d.Next(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := d.Write(context.Background(), []float32{.5, -.5}); err != nil {
				t.Fatal(err)
			}
			first := f.clock + int64(time.Millisecond)
			base := time.Now()
			calls := 0
			d.now = func() time.Time {
				calls++
				if calls > 1 {
					if name == "slow" {
						return base.Add(time.Second)
					}
					if name == "backwards" {
						return base.Add(-time.Second)
					}
				}
				return base
			}
			switch name {
			case "zero":
				first = 0
			case "too-far":
				first += int64(3 * time.Second)
			case "too-old":
				first = 1
				f.clock = int64(3 * time.Second)
			case "no-clock":
				f.clock = 0
			}
			out := make([]float32, 96)
			if name == "invalid-frame" {
				out = make([]float32, 3)
			}
			d.Process(out, first)
			if !slices.Equal(out, make([]float32, len(out))) {
				t.Fatal("media escaped invalid timeline")
			}
			if _, err := d.Next(context.Background()); err == nil {
				t.Fatal("invalid timing accepted")
			}
			d.now = time.Now
		})
	}
}
func TestCancellationAndCloseWakeQueueWaiters(t *testing.T) {
	f := &fakeDriver{}
	d := openFake(t, f)
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := d.Write(ctx, make([]float32, len(d.ring)+96)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Write(context.Background(), make([]float32, 96)) }()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := d.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if f.closes != 1 || f.stops != 1 {
		t.Fatal("native handle not joined exactly once", f.closes, f.stops)
	}
	if _, err := d.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestStopRetainsCanceledTailAndRestartsLazily(t *testing.T) {
	f := &fakeDriver{}
	d := openFake(t, f)
	base := time.Now()
	d.now = func() time.Time { return base }
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.Process(make([]float32, 96), f.clock+int64(20*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := d.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if d.tail.IsZero() || d.started {
		t.Fatal("canceled flush lost tail or did not stop")
	}
	at := time.Now()
	if err := d.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(at) < 20*time.Millisecond || !d.tail.IsZero() {
		t.Fatal("flush retry skipped original tail")
	}
	if f.starts != 1 {
		t.Fatal("flush restarted device before Next")
	}
	if _, err := d.Next(context.Background()); err != nil || f.starts != 2 {
		t.Fatal("restart", err)
	}
}
func TestDriverFailureAndStartupBudget(t *testing.T) {
	for _, name := range []string{"start", "check", "callback", "timeout", "stop", "close"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDriver{}
			d := openFake(t, f)
			fault := errors.New("native failure")
			switch name {
			case "start":
				f.start = fault
			case "check":
				f.check = fault
			case "timeout":
				f.silent = true
			}
			_, err := d.Next(context.Background())
			if name == "start" || name == "check" || name == "timeout" {
				if err == nil {
					t.Fatal("startup failure ignored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "callback":
				d.Fail(ErrDriver)
				if _, err = d.Next(context.Background()); !errors.Is(err, ErrDriver) {
					t.Fatal(err)
				}
			case "stop":
				f.stop = fault
				if err = d.Flush(context.Background()); !errors.Is(err, fault) {
					t.Fatal(err)
				}
			case "close":
				f.close = fault
				if err = d.Close(); !errors.Is(err, fault) {
					t.Fatal(err)
				}
				if err = d.Close(); !errors.Is(err, fault) || f.closes != 1 {
					t.Fatal("close error/idempotence lost", err)
				}
			}
		})
	}
}
func TestValidationAndCanceledCallsDoNotStartNativeIO(t *testing.T) {
	f := &fakeDriver{}
	d := openFake(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := d.Write(ctx, []float32{0, 0}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := d.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.starts != 0 {
		t.Fatal("canceled calls reached driver")
	}
	for _, samples := range [][]float32{{0}, {float32(math.NaN()), 0}, {float32(math.Inf(1)), 0}} {
		if err := d.Write(context.Background(), samples); err == nil {
			t.Fatal("invalid sample accepted")
		}
	}
	if f.starts != 0 {
		t.Fatal("invalid samples started driver")
	}
	for _, c := range []Config{{Device: "x\x00y"}, {BufferDuration: -1}, {StartupTimeout: time.Hour}, {MaxTimingWindow: -1}} {
		if _, err := c.Normalize(audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	for _, format := range []audio.Format{{}, {SampleRate: 48000, Channels: 9}, {SampleRate: 9999999, Channels: 2}} {
		if _, err := Open(context.Background(), Config{}, format, nil); err == nil {
			t.Fatal("invalid format accepted")
		}
	}
}

func TestBlockedWriteChecksNativeHealthWithoutCallbacks(t *testing.T) {
	f := &fakeDriver{failAfter: 3}
	d := openFake(t, f)
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Write(ctx, make([]float32, len(d.ring)+96)); !errors.Is(err, ErrDriver) {
		t.Fatal("health failure did not unblock writer", err)
	}
}

func TestStaleCallbackClockInvalidatesQueuedMedia(t *testing.T) {
	f := &fakeDriver{}
	d := openFake(t, f)
	base := time.Now()
	d.now = func() time.Time { return base }
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.now = func() time.Time { return base.Add(2 * time.Second) }
	if err := d.Write(context.Background(), []float32{.5, -.5}); !errors.Is(err, ErrTiming) {
		t.Fatal(err)
	}
	out := make([]float32, 96)
	d.Process(out, f.clock+int64(time.Millisecond))
	if !slices.Equal(out, make([]float32, 96)) {
		t.Fatal("stale media escaped")
	}
	d.now = time.Now
}

func TestFactoryFailureAndPostOpenCancellationReleaseDriver(t *testing.T) {
	format := audio.Format{SampleRate: 48000, Channels: 2}
	fault := errors.New("open failure")
	f := &fakeDriver{}
	if _, err := Open(context.Background(), Config{}, format, func(*Device) (Driver, error) { return f, fault }); !errors.Is(err, fault) || f.closes != 1 {
		t.Fatal("partial driver leaked", err, f.closes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f = &fakeDriver{}
	if _, err := Open(ctx, Config{Logger: recording.NewLogger()}, format, func(*Device) (Driver, error) { cancel(); return f, nil }); !errors.Is(err, context.Canceled) || f.closes != 1 || f.starts != 0 {
		t.Fatal("post-open cancellation leaked/started driver", err)
	}
}

func TestLifecycleLoggerRedactsDeviceAndRawNativeFailure(t *testing.T) {
	l := recording.NewLogger()
	l.SetLevel(level.Debug)
	f := &fakeDriver{close: errors.New("SECRET native failure")}
	d, err := Open(context.Background(), Config{Backend: "fake", Device: "SECRET endpoint", Logger: l}, audio.Format{SampleRate: 48000, Channels: 2}, func(*Device) (Driver, error) { return f, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err == nil {
		t.Fatal("native close failure lost")
	}
	if l.Len() != 2 {
		t.Fatal("lifecycle log count", l.Len())
	}
	for _, event := range l.GetAll() {
		if err := event.ForEach(func(key string, value any) error {
			if s, ok := value.(string); ok && strings.Contains(s, "SECRET") {
				t.Fatal("private native data logged", key)
			}
			if key == "error" {
				t.Fatal("raw error logged")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
