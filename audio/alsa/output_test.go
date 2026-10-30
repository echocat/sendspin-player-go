package alsa

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

var _ audio.Output = Output{}
var _ audio.Device = (*device)(nil)

type fakePCM struct {
	state                           pcmState
	delay                           int64
	stamp, now                      int64
	snapshotErr                     error
	writeErr                        error
	dropErr                         error
	prepareErr                      error
	startErr                        error
	closeErr                        error
	limit                           int
	writes                          int
	drops, prepares, starts, closes int
	data                            []byte
	onWrite                         func()
	onSnapshot                      func()
	writeResults                    []struct {
		n   int
		err error
	}
}

func TestStructuredLoggerOmitsDeviceAndRawErrors(t *testing.T) {
	l := recording.NewLogger()
	l.SetLevel(level.Debug)
	p := &fakePCM{closeErr: errors.New("SECRET native path")}
	d, err := openWith(context.Background(), Output{Device: "SECRET device", Logger: l}, audio.Format{SampleRate: 48000, Channels: 2}, func(context.Context, Output, audio.Format) (pcm, negotiated, error) {
		return p, negotiated{info: Info{BufferFrames: 480, PeriodFrames: 48}, encoding: encodingS16, hardware: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err == nil {
		t.Fatal("native close error lost")
	}
	if l.Len() != 2 {
		t.Fatal("missing lifecycle logging", l.Len())
	}
	for _, event := range l.GetAll() {
		if err := event.ForEach(func(key string, value any) error {
			if text, ok := value.(string); ok && strings.Contains(text, "SECRET") {
				t.Fatal("private log value", key)
			}
			if key == "error" {
				t.Fatal("raw error in backend log")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func (p *fakePCM) snapshot() (nativeSnapshot, error) {
	if p.onSnapshot != nil {
		p.onSnapshot()
	}
	return nativeSnapshot{state: p.state, delay: p.delay, stamp: p.stamp, now: p.now}, p.snapshotErr
}
func (p *fakePCM) write(data []byte, frames int) (int, error) {
	p.writes++
	if p.onWrite != nil {
		p.onWrite()
	}
	if len(p.writeResults) > 0 {
		result := p.writeResults[0]
		p.writeResults = p.writeResults[1:]
		return result.n, result.err
	}
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	n := frames
	if p.limit > 0 {
		n = min(n, p.limit)
	}
	p.data = append(p.data, data[:len(data)*n/frames]...)
	return n, nil
}
func (p *fakePCM) drop() error {
	p.drops++
	if p.dropErr != nil {
		return p.dropErr
	}
	p.state = stateSetup
	return nil
}
func (p *fakePCM) prepare() error { p.prepares++; p.state = statePrepared; return p.prepareErr }
func (p *fakePCM) start() error   { p.starts++; p.state = stateRunning; p.delay = 480; return p.startErr }
func (p *fakePCM) close() error   { p.closes++; return p.closeErr }

func testDevice(t *testing.T, p *fakePCM) *device {
	t.Helper()
	if p.state == stateUnknown {
		p.state = statePrepared
	}
	p.stamp, p.now = int64(time.Second), int64(time.Second)
	o := Output{Logger: recording.NewLogger()}
	d, err := openWith(context.Background(), o, audio.Format{SampleRate: 48000, Channels: 2}, func(context.Context, Output, audio.Format) (pcm, negotiated, error) {
		return p, negotiated{info: Info{BufferFrames: 480, PeriodFrames: 48}, encoding: encodingFloat, hardware: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.(*device)
}

func TestLazyStartupAndNativeClockCorrelation(t *testing.T) {
	p := &fakePCM{}
	d := testDevice(t, p)
	if p.writes != 0 || p.starts != 0 {
		t.Fatal("Open started playback")
	}
	base := time.Now()
	d.now = func() time.Time { return base }
	next, err := d.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if next.Sub(base) != 10*time.Millisecond || p.starts != 1 || p.writes != 10 || !slices.Equal(p.data, make([]byte, 480*2*4)) {
		t.Fatal("missing bounded silence/native timeline", next.Sub(base), p.starts, p.writes)
	}
	p.delay = 240
	p.now += int64(time.Millisecond)
	next, err = d.Next(context.Background())
	if err != nil || next.Sub(base) != 4*time.Millisecond || p.starts != 1 {
		t.Fatal("Next used a sample counter instead of the native snapshot", next.Sub(base), err)
	}
	p.state = stateXrun
	if _, err = d.Next(context.Background()); err != nil || p.starts != 2 {
		t.Fatal("Next did not rebuild underrun timeline", err)
	}
}

func TestTimingRejectsInvalidSnapshots(t *testing.T) {
	for _, name := range []string{"negative-delay", "excessive-delay", "zero-timestamp", "future-timestamp", "stale-timestamp", "suspended", "unknown-state", "slow-correlation", "backwards-correlation", "io-error"} {
		t.Run(name, func(t *testing.T) {
			p := &fakePCM{state: stateRunning, delay: 48}
			d := testDevice(t, p)
			base := time.Now()
			calls := 0
			d.now = func() time.Time {
				calls++
				if calls > 1 {
					if name == "slow-correlation" {
						return base.Add(3 * time.Millisecond)
					}
					if name == "backwards-correlation" {
						return base.Add(-time.Microsecond)
					}
				}
				return base
			}
			switch name {
			case "negative-delay":
				p.delay = -1
			case "excessive-delay":
				p.delay = 96001
			case "zero-timestamp":
				p.stamp = 0
			case "future-timestamp":
				p.stamp = p.now + 1
			case "stale-timestamp":
				p.now += int64(3 * time.Second)
			case "suspended":
				p.state = stateSuspended
			case "unknown-state":
				p.state = stateUnknown
			case "io-error":
				p.snapshotErr = errors.New("device lost")
			}
			if _, err := d.Next(context.Background()); err == nil {
				t.Fatal("invalid timing accepted")
			}
			if p.starts != 0 {
				t.Fatal("invalid timing silently restarted PCM")
			}
		})
	}
}

func TestWritePartialOwnershipAndValidation(t *testing.T) {
	p := &fakePCM{limit: 2}
	d := testDevice(t, p)
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.data = nil
	samples := []float32{.25, -.25, .5, -.5, 1, -1, .75, -.75}
	expected := encodeSamples(samples, encodingFloat)
	if err := d.Write(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	clear(samples)
	if !slices.Equal(expected, p.data) {
		t.Fatal("partial writes lost sample order or retained caller data")
	}
	for _, invalid := range [][]float32{{0}, {float32(math.NaN()), 0}, {float32(math.Inf(1)), 0}} {
		n := p.writes
		if err := d.Write(context.Background(), invalid); err == nil || p.writes != n {
			t.Fatal("invalid samples reached native write")
		}
	}
	if err := d.Write(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRetryCancellationAndClockReset(t *testing.T) {
	for _, name := range []string{"again", "interrupted", "underrun", "io-error", "invalid-count"} {
		t.Run(name, func(t *testing.T) {
			p := &fakePCM{}
			d := testDevice(t, p)
			if _, err := d.Next(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
			defer cancel()
			switch name {
			case "again":
				p.writeErr = errAgain
			case "interrupted":
				p.writeErr = errInterrupted
			case "underrun":
				p.writeErr = errXrun
			case "io-error":
				p.writeErr = errors.New("disconnected")
			case "invalid-count":
				p.writeResults = []struct {
					n   int
					err error
				}{{n: 1000}}
			}
			err := d.Write(ctx, make([]float32, 96))
			switch name {
			case "again", "interrupted":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case "underrun":
				if !errors.Is(err, ErrClockReset) {
					t.Fatal(err)
				}
			default:
				if err == nil {
					t.Fatal("write fault ignored")
				}
			}
			if p.starts != 1 {
				t.Fatal("Write silently changed the Next timeline")
			}
		})
	}
	p := &fakePCM{}
	d := testDevice(t, p)
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.writeResults = []struct {
		n   int
		err error
	}{{err: errAgain}, {err: errInterrupted}}
	if err := d.Write(context.Background(), make([]float32, 96)); err != nil {
		t.Fatal("transient retry", err)
	}
}

func TestCanceledOperationsNeverEnterNativeCode(t *testing.T) {
	p := &fakePCM{}
	d := testDevice(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := d.Write(ctx, make([]float32, 96)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := d.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if p.writes != 0 || p.drops != 0 {
		t.Fatal("canceled call touched PCM")
	}
	called := false
	if _, err := openWith(ctx, Output{}, audio.Format{SampleRate: 48000, Channels: 2}, func(context.Context, Output, audio.Format) (pcm, negotiated, error) {
		called = true
		return nil, negotiated{}, nil
	}); !errors.Is(err, context.Canceled) || called {
		t.Fatal(err, called)
	}
}

func TestFlushDropsWaitsAndReprimes(t *testing.T) {
	p := &fakePCM{}
	d := testDevice(t, p)
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if err := d.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(before) < 9*time.Millisecond || d.timed || d.started || p.state != statePrepared {
		t.Fatal("Flush did not stop/drop/wait before returning")
	}
	if _, err := d.Next(context.Background()); err != nil || p.starts != 2 {
		t.Fatal("Flush did not reset startup", err)
	}
}

func TestCanceledFlushRetainsTailAndUnknownTimingFailsClosed(t *testing.T) {
	p := &fakePCM{}
	d := testDevice(t, p)
	base := time.Now()
	d.now = func() time.Time { return base }
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel during stop's snapshot, after Flush acquires the lock, so timer
	// scheduling cannot let the output tail finish before cancellation.
	p.onSnapshot = cancel
	if err := d.Flush(ctx); !errors.Is(err, context.Canceled) || d.tail.IsZero() {
		t.Fatal("canceled stop lost downstream tail", err)
	}
	p.onSnapshot = nil
	before := time.Now()
	if err := d.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(before) < 5*time.Millisecond || !d.tail.IsZero() {
		t.Fatal("retry skipped original output tail")
	}
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.snapshotErr = errors.New("status lost")
	if err := d.Flush(context.Background()); err == nil {
		t.Fatal("Flush invented timing on status failure")
	}
	if err := d.Close(); err == nil || p.closes != 1 {
		t.Fatal("Close forgot unknown downstream timing or leaked PCM", err)
	}
}

func TestConcurrentCloseInterruptsBlockedWriteAndIsIdempotent(t *testing.T) {
	p := &fakePCM{}
	d := testDevice(t, p)
	if _, err := d.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var enter sync.Once
	p.writeErr = errAgain
	p.onWrite = func() { enter.Do(func() { close(entered) }) }
	done := make(chan error, 1)
	go func() { done <- d.Write(context.Background(), make([]float32, 96)) }()
	<-entered
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
	if p.closes != 1 {
		t.Fatal("multiple native closes", p.closes)
	}
	if _, err := d.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := d.Flush(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestOpenPolicyValidationAndCleanup(t *testing.T) {
	format := audio.Format{SampleRate: 48000, Channels: 2}
	for _, name := range []string{"plugin-denied", "plugin-allowed", "invalid-buffer", "invalid-period", "invalid-encoding", "canceled-after-open", "open-failure"} {
		t.Run(name, func(t *testing.T) {
			p := &fakePCM{}
			o := Output{Logger: recording.NewLogger()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "plugin-allowed" {
				o.AllowPluginTiming = true
			}
			d, err := openWith(ctx, o, format, func(context.Context, Output, audio.Format) (pcm, negotiated, error) {
				if name == "open-failure" {
					return nil, negotiated{}, errors.New("unavailable")
				}
				config := negotiated{info: Info{BufferFrames: 480, PeriodFrames: 48}, encoding: encodingS16, hardware: true}
				switch name {
				case "plugin-denied", "plugin-allowed":
					config.hardware = false
				case "invalid-buffer":
					config.info.BufferFrames = 0
				case "invalid-period":
					config.info.PeriodFrames = 481
				case "invalid-encoding":
					config.encoding = 0
				case "canceled-after-open":
					cancel()
				}
				return p, config, nil
			})
			if name == "plugin-allowed" {
				if err != nil || !d.(*device).Info().Approximate {
					t.Fatal(err)
				}
				if err = d.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid open accepted")
			}
			if name == "plugin-denied" && !errors.Is(err, ErrPluginTiming) {
				t.Fatal(err)
			}
			if name != "open-failure" && p.closes != 1 {
				t.Fatal("failed open leaked PCM")
			}
		})
	}
	for _, o := range []Output{{Device: "a\x00b"}, {BufferDuration: -1}, {BufferDuration: time.Second}, {PeriodDuration: time.Second}, {MaxTimingWindow: -1}} {
		if _, err := o.defaults(); err == nil {
			t.Fatal("bad configuration accepted", o)
		}
	}
	for _, f := range []audio.Format{{SampleRate: 0, Channels: 2}, {SampleRate: 48000, Channels: 0}, {SampleRate: 48000, Channels: 33}, {SampleRate: 384001, Channels: 2}} {
		if _, err := openWith(context.Background(), Output{}, f, nil); err == nil {
			t.Fatal("bad format accepted", f)
		}
	}
}

func TestPrimeCancellationAndFailures(t *testing.T) {
	for _, name := range []string{"prepare", "drop", "start", "write", "cancel-during-prime"} {
		t.Run(name, func(t *testing.T) {
			p := &fakePCM{}
			d := testDevice(t, p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fault := errors.New("fault")
			switch name {
			case "prepare":
				p.prepareErr = fault
			case "drop":
				p.dropErr = fault
			case "start":
				p.startErr = fault
			case "write":
				p.writeErr = fault
			case "cancel-during-prime":
				p.onWrite = cancel
			}
			if _, err := d.Next(ctx); err == nil {
				t.Fatal("startup failure ignored")
			}
		})
	}
}
