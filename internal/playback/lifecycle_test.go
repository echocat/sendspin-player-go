package playback

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

// Only the worker touches the fixture's slices/counters. Tests inspect them
// after Run joins; channels select exact lifecycle boundaries without sleeps.
type lifecycleOutput struct {
	at       time.Time
	opens    []audio.Format
	devices  []*lifecycleDevice
	open     func(context.Context, audio.Format) error
	next     func(context.Context, *lifecycleDevice) (time.Time, error)
	write    func(context.Context, *lifecycleDevice, []float32) error
	accepted func(*lifecycleDevice, []float32)
	flush    func(context.Context, *lifecycleDevice) error
	close    func(*lifecycleDevice) error
}
type lifecycleDevice struct {
	output          *lifecycleOutput
	format          audio.Format
	writes          [][]float32
	flushes, closes int
}

func (o *lifecycleOutput) Open(ctx context.Context, format audio.Format) (audio.Device, error) {
	o.opens = append(o.opens, format)
	if o.open != nil {
		if err := o.open(ctx, format); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &lifecycleDevice{output: o, format: format}
	o.devices = append(o.devices, d)
	return d, nil
}
func (d *lifecycleDevice) Next(ctx context.Context) (time.Time, error) {
	if d.output.next != nil {
		return d.output.next(ctx, d)
	}
	return d.output.at, ctx.Err()
}
func (d *lifecycleDevice) Write(ctx context.Context, samples []float32) error {
	if d.output.write != nil {
		if err := d.output.write(ctx, d, samples); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.writes = append(d.writes, append([]float32(nil), samples...))
	if d.output.accepted != nil {
		d.output.accepted(d, samples)
	}
	return nil
}
func (d *lifecycleDevice) Flush(ctx context.Context) error {
	d.flushes++
	if d.output.flush != nil {
		return d.output.flush(ctx, d)
	}
	return ctx.Err()
}
func (d *lifecycleDevice) Close() error {
	d.closes++
	if d.output.close != nil {
		return d.output.close(d)
	}
	return nil
}

func lifecycleChunk(rate int, value float32) Chunk {
	samples := make([]float32, 960)
	for i := range samples {
		samples[i] = value
	}
	return Chunk{Format: audio.Format{SampleRate: rate, Channels: 1}, Samples: samples, WireBytes: 1933}
}
func lifecycleEngine(t *testing.T, o *lifecycleOutput, chunks ...Chunk) *Engine {
	t.Helper()
	e := New(o, 1<<20, func(int64) (time.Time, error) { return o.at, nil })
	for _, c := range chunks {
		if err := e.Enqueue(c); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestFormatDrainErrorsStopBeforeReopening(t *testing.T) {
	for _, failure := range []error{errors.New("lost output clock"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			o := &lifecycleOutput{at: time.Now()}
			o.next = func(ctx context.Context, d *lifecycleDevice) (time.Time, error) {
				if len(d.writes) > 0 {
					return time.Time{}, failure
				}
				return o.at, ctx.Err()
			}
			e := lifecycleEngine(t, o, lifecycleChunk(48000, .25), lifecycleChunk(96000, .5))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := e.Run(ctx)
			if !errors.Is(err, failure) || ctx.Err() != nil {
				t.Fatal("format-drain failure was hidden until timeout", err)
			}
			if len(o.opens) != 1 || len(o.devices) != 1 || o.devices[0].closes != 1 {
				t.Fatal("failed drain reopened output or repeated cleanup", o.opens)
			}
		})
	}
}

func TestFormatCloseFailureIsNotRetried(t *testing.T) {
	failure := errors.New("close failed")
	o := &lifecycleOutput{at: time.Now(), close: func(*lifecycleDevice) error { return failure }}
	e := lifecycleEngine(t, o, lifecycleChunk(48000, .25), lifecycleChunk(96000, .5))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Run(ctx); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(o.opens) != 1 || o.devices[0].closes != 1 {
		t.Fatal("ownership was retained after attempted close", o.devices[0].closes)
	}
}

func TestBackendCanceledErrorDoesNotMasqueradeAsReset(t *testing.T) {
	for _, phase := range []string{"open", "next", "write"} {
		t.Run(phase, func(t *testing.T) {
			o := &lifecycleOutput{at: time.Now()}
			switch phase {
			case "open":
				o.open = func(context.Context, audio.Format) error { return context.Canceled }
			case "next":
				o.next = func(context.Context, *lifecycleDevice) (time.Time, error) { return time.Time{}, context.Canceled }
			case "write":
				o.write = func(context.Context, *lifecycleDevice, []float32) error { return context.Canceled }
			}
			e := lifecycleEngine(t, o, lifecycleChunk(48000, .25))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := e.Run(ctx); !errors.Is(err, context.Canceled) || ctx.Err() != nil {
				t.Fatal("independent backend failure was swallowed", err)
			}
		})
	}
}

func TestResetDuringFormatDrainSkipsObsoleteOpenAndFlushesBeforeFreshAudio(t *testing.T) {
	o := &lifecycleOutput{at: time.Now()}
	draining := make(chan struct{})
	fresh := make(chan struct{})
	freshBeforeFlush := false
	firstDrain := true
	o.next = func(ctx context.Context, d *lifecycleDevice) (time.Time, error) {
		if len(d.writes) > 0 && firstDrain {
			firstDrain = false
			close(draining)
			<-ctx.Done()
			return time.Time{}, ctx.Err()
		}
		return o.at, ctx.Err()
	}
	o.write = func(_ context.Context, d *lifecycleDevice, samples []float32) error {
		if samples[0] == .75 {
			freshBeforeFlush = d.flushes == 0
		}
		return nil
	}
	o.accepted = func(_ *lifecycleDevice, samples []float32) {
		if samples[0] == .75 {
			close(fresh)
		}
	}
	e := lifecycleEngine(t, o, lifecycleChunk(48000, .25), lifecycleChunk(96000, .5))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-draining:
	case <-ctx.Done():
		t.Fatal("format drain not reached")
	}
	e.Reset()
	if err := e.Enqueue(lifecycleChunk(48000, .75)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fresh:
	case <-ctx.Done():
		t.Fatal("fresh audio did not resume")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(o.opens) != 1 || len(o.devices) != 1 {
		t.Fatal("canceled format was reopened", o.opens)
	}
	d := o.devices[0]
	if freshBeforeFlush || d.flushes != 1 || len(d.writes) != 2 || d.writes[0][0] != .25 || d.writes[1][0] != .75 || d.closes != 1 {
		t.Fatal("reset did not preserve ordered old/flush/fresh ownership", fmt.Sprint(d.flushes, len(d.writes), d.closes))
	}
}

func TestResetDropsBlockedAndQueuedAudioBeforeFreshOutput(t *testing.T) {
	for _, flushFails := range []bool{false, true} {
		t.Run(fmt.Sprint("flush-fails=", flushFails), func(t *testing.T) {
			o := &lifecycleOutput{at: time.Now()}
			entered, fresh := make(chan struct{}), make(chan struct{})
			failure := errors.New("flush failed")
			freshBeforeFlush := false
			o.write = func(ctx context.Context, d *lifecycleDevice, samples []float32) error {
				if samples[0] == .25 {
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				}
				if samples[0] == .75 {
					freshBeforeFlush = d.flushes == 0
				}
				return nil
			}
			o.accepted = func(_ *lifecycleDevice, samples []float32) {
				if samples[0] == .75 {
					close(fresh)
				}
			}
			if flushFails {
				o.flush = func(context.Context, *lifecycleDevice) error { return failure }
			}
			e := lifecycleEngine(t, o, lifecycleChunk(48000, .25), lifecycleChunk(48000, .5))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- e.Run(ctx) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("blocked write not reached")
			}
			e.Reset()
			if err := e.Enqueue(lifecycleChunk(48000, .75)); err != nil {
				t.Fatal(err)
			}
			if flushFails {
				if err := <-done; !errors.Is(err, failure) {
					t.Fatal("flush failure hidden", err)
				}
			} else {
				select {
				case <-fresh:
				case <-ctx.Done():
					t.Fatal("fresh audio never resumed")
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			if len(o.devices) != 1 || o.devices[0].closes != 1 || o.devices[0].flushes != 1 {
				t.Fatal("unexpected reset lifecycle")
			}
			d := o.devices[0]
			if flushFails {
				if len(d.writes) != 0 {
					t.Fatal("accepted output after failed flush")
				}
			} else if freshBeforeFlush || len(d.writes) != 1 || d.writes[0][0] != .75 {
				t.Fatal("old blocked/queued audio survived reset")
			}
		})
	}
}

func TestCanceledWorkerNeverOpensQueuedAudio(t *testing.T) {
	o := &lifecycleOutput{at: time.Now()}
	e := lifecycleEngine(t, o, lifecycleChunk(48000, .25))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Run(ctx); !errors.Is(err, context.Canceled) || len(o.opens) != 0 {
		t.Fatal("canceled worker touched the output", err, o.opens)
	}
}

func TestResetCancelsPendingOpenButKeepsTheWorkerUsable(t *testing.T) {
	o := &lifecycleOutput{at: time.Now()}
	opening, fresh := make(chan struct{}), make(chan struct{})
	first := true
	o.open = func(ctx context.Context, _ audio.Format) error {
		if first {
			first = false
			close(opening)
			<-ctx.Done()
			return ctx.Err()
		}
		return ctx.Err()
	}
	o.accepted = func(*lifecycleDevice, []float32) { close(fresh) }
	e := lifecycleEngine(t, o, lifecycleChunk(48000, .25), lifecycleChunk(96000, .5))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-opening:
	case <-ctx.Done():
		t.Fatal("Open did not start")
	}
	e.Reset()
	if err := e.Enqueue(lifecycleChunk(48000, .75)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fresh:
	case <-ctx.Done():
		t.Fatal("worker treated reset cancellation as a terminal Open failure")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(o.opens) != 2 || o.opens[0] != o.opens[1] || len(o.devices) != 1 || o.devices[0].closes != 1 || len(o.devices[0].writes) != 1 || o.devices[0].writes[0][0] != .75 {
		t.Fatal("canceled Open resurrected old or queued audio", o.opens)
	}
}
