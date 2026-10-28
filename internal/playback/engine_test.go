package playback

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

type testDevice struct {
	next   time.Time
	writes [][]float32
}

func (d *testDevice) Next(ctx context.Context) (time.Time, error) { return d.next, ctx.Err() }
func (d *testDevice) Write(ctx context.Context, v []float32) error {
	d.writes = append(d.writes, append([]float32(nil), v...))
	return ctx.Err()
}
func (d *testDevice) Flush(context.Context) error { return nil }
func (d *testDevice) Close() error                { return nil }

func TestSoftCorrectionBoundAndGain(t *testing.T) {
	for _, delta := range []time.Duration{-900 * time.Microsecond, -200 * time.Microsecond, 0, 200 * time.Microsecond, 900 * time.Microsecond} {
		t.Run(delta.String(), func(t *testing.T) {
			target := time.Now().Add(time.Second)
			d := &testDevice{next: target.Add(-delta)}
			e := New(nil, 1<<20, func(int64) (time.Time, error) { return target, nil })
			samples := make([]float32, 960*2)
			for i := range samples {
				samples[i] = 0.25
			}
			gain := float32(1)
			if err := e.render(context.Background(), d, Chunk{Format: audio.Format{SampleRate: 48000, Channels: 2}, Samples: samples}, &gain); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, v := range d.writes {
				count += len(v) / 2
				for _, sample := range v {
					if sample != 0.25 {
						t.Fatal("changed untouched sample", sample)
					}
				}
			}
			if math.Abs(float64(count-960))/960 > 0.005 {
				t.Fatal("speed deviation", count)
			}
		})
	}
}
func TestLateAudioIsDropped(t *testing.T) {
	target := time.Now()
	d := &testDevice{next: target.Add(100 * time.Millisecond)}
	e := New(nil, 65535, func(int64) (time.Time, error) { return target, nil })
	gain := float32(1)
	if err := e.render(context.Background(), d, Chunk{Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: make([]float32, 960)}, &gain); err != nil {
		t.Fatal(err)
	}
	if len(d.writes) != 0 {
		t.Fatal("played stale audio")
	}
}

func TestExtremePastAudioCannotOverflowLateDrop(t *testing.T) {
	// time.Time.Sub saturates at MinInt64 for targets more than ~292 years
	// behind the device. Negating that duration must not create a negative
	// slice index or overflow a platform int while computing a late prefix.
	device := &testDevice{next: time.Now()}
	engine := New(nil, 65535, func(int64) (time.Time, error) { return time.Time{}, nil })
	gain := float32(1)
	if err := engine.render(context.Background(), device, Chunk{Format: audio.Format{SampleRate: 384000, Channels: 2}, Samples: make([]float32, 960*2)}, &gain); err != nil {
		t.Fatal(err)
	}
	if len(device.writes) != 0 {
		t.Fatal("played an extremely stale chunk")
	}
}

func TestLateAudioDropsOnlyTheExpiredPrefix(t *testing.T) {
	for _, test := range []struct{ rate, channels, frames, drop int }{{8000, 1, 160, 40}, {44100, 2, 882, 220}, {48000, 2, 960, 240}, {384000, 8, 7680, 1920}} {
		t.Run(fmt.Sprint(test.rate), func(t *testing.T) {
			target := time.Now()
			device := &testDevice{next: target.Add(5 * time.Millisecond)}
			engine := New(nil, 1<<20, func(int64) (time.Time, error) { return target, nil })
			samples := make([]float32, test.frames*test.channels)
			for i := range samples {
				samples[i] = float32(i) / float32(len(samples))
			}
			gain := float32(1)
			if err := engine.render(context.Background(), device, Chunk{Format: audio.Format{SampleRate: test.rate, Channels: test.channels}, Samples: samples}, &gain); err != nil {
				t.Fatal(err)
			}
			if len(device.writes) != 1 || len(device.writes[0]) != (test.frames-test.drop)*test.channels {
				t.Fatal("incorrect late prefix length")
			}
			for i, sample := range device.writes[0] {
				if sample != samples[i+test.drop*test.channels] {
					t.Fatal("late prefix recovery changed remaining audio")
				}
			}
		})
	}
}

func TestPerceptualGainAndMuteUseAShortRamp(t *testing.T) {
	target := time.Now()
	device := &testDevice{next: target}
	engine := New(nil, 65535, func(int64) (time.Time, error) { return target, nil })
	samples := make([]float32, 960)
	for i := range samples {
		samples[i] = 1
	}
	chunk := Chunk{Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: samples}
	gain := float32(1)
	for _, state := range []struct {
		volume int
		muted  bool
		want   float32
	}{{50, false, float32(math.Pow(0.5, 1.5))}, {50, true, 0}, {25, true, 0}, {25, false, float32(math.Pow(0.25, 1.5))}} {
		engine.State(state.volume, state.muted, 0)
		start := gain
		if err := engine.render(context.Background(), device, chunk, &gain); err != nil {
			t.Fatal(err)
		}
		write := device.writes[len(device.writes)-1]
		if len(write) != len(samples) {
			t.Fatal("gain changed sample count")
		}
		for i, v := range write {
			want := state.want
			if i < 240 {
				want = start + (state.want-start)*float32(i+1)/240
			}
			if v != want {
				t.Fatalf("volume %d muted %v frame %d: gain %g, want %g", state.volume, state.muted, i, v, want)
			}
		}
		if gain != state.want {
			t.Fatalf("ramp ended at %g, want %g", gain, state.want)
		}
	}
	for _, sample := range samples {
		if sample != 1 {
			t.Fatal("gain mutated decoder-owned samples")
		}
	}
}
func TestOverflowRecoversWithinMemoryLimits(t *testing.T) {
	e := New(nil, 100, func(int64) (time.Time, error) { return time.Now(), nil })
	chunk := Chunk{WireBytes: 80, Samples: make([]float32, 20)}
	if err := e.Enqueue(chunk); err != nil {
		t.Fatal(err)
	}
	if err := e.Enqueue(chunk); err != nil {
		t.Fatal("temporary excess disconnected", err)
	}
	if len(e.queue) != 1 || e.bytes != 80 || e.generation != 1 {
		t.Fatal("overflow did not reset bounded queue")
	}
}

type blockingOutput struct {
	started chan struct{}
	flushed chan struct{}
	once    sync.Once
}
type blockingDevice struct{ owner *blockingOutput }

func (o *blockingOutput) Open(context.Context, audio.Format) (audio.Device, error) {
	return &blockingDevice{o}, nil
}
func (d *blockingDevice) Next(ctx context.Context) (time.Time, error) { return time.Now(), ctx.Err() }
func (d *blockingDevice) Write(ctx context.Context, _ []float32) error {
	d.owner.once.Do(func() { close(d.owner.started) })
	<-ctx.Done()
	return ctx.Err()
}
func (d *blockingDevice) Flush(context.Context) error {
	select {
	case d.owner.flushed <- struct{}{}:
	default:
	}
	return nil
}
func (d *blockingDevice) Close() error { return nil }
func TestResetCancelsAnInFlightWrite(t *testing.T) {
	o := &blockingOutput{started: make(chan struct{}), flushed: make(chan struct{}, 1)}
	e := New(o, 65535, func(int64) (time.Time, error) { return time.Now(), nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	if err := e.Enqueue(Chunk{Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: make([]float32, 960), WireBytes: 1933}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-o.started:
	case <-time.After(time.Second):
		t.Fatal("Write did not start")
	}
	e.Reset()
	select {
	case <-o.flushed:
	case <-time.After(time.Second):
		t.Fatal("reset did not flush")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker leaked")
	}
}

type formatOutput struct {
	mu       sync.Mutex
	targets  map[audio.Format]time.Time
	opened   []audio.Format
	writes   []Chunk
	closedAt time.Time
	flushed  bool
	finished chan struct{}
}
type formatDevice struct {
	owner  *formatOutput
	format audio.Format
	next   time.Time
}

func (o *formatOutput) Open(_ context.Context, format audio.Format) (audio.Device, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opened = append(o.opened, format)
	return &formatDevice{owner: o, format: format, next: o.targets[format]}, nil
}
func (d *formatDevice) Next(ctx context.Context) (time.Time, error) { return d.next, ctx.Err() }
func (d *formatDevice) Write(ctx context.Context, samples []float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.next = d.next.Add(time.Duration(len(samples)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
	d.owner.mu.Lock()
	defer d.owner.mu.Unlock()
	d.owner.writes = append(d.owner.writes, Chunk{Format: d.format, Samples: append([]float32(nil), samples...)})
	if len(d.owner.writes) == 2 {
		close(d.owner.finished)
	}
	return nil
}
func (d *formatDevice) Flush(context.Context) error {
	d.owner.mu.Lock()
	d.owner.flushed = true
	d.owner.mu.Unlock()
	return nil
}
func (d *formatDevice) Close() error {
	d.owner.mu.Lock()
	if len(d.owner.opened) == 1 {
		d.owner.closedAt = monotime.Now()
	}
	d.owner.mu.Unlock()
	return nil
}

func TestFormatChangeRetainsQueuedAudioAndDrainsOldDevice(t *testing.T) {
	old := audio.Format{SampleRate: 48000, Channels: 1}
	next := audio.Format{SampleRate: 96000, Channels: 2}
	start := monotime.Now().Add(20 * time.Millisecond)
	boundary := start.Add(20 * time.Millisecond)
	output := &formatOutput{targets: map[audio.Format]time.Time{old: start, next: boundary}, finished: make(chan struct{})}
	engine := New(output, 65535, func(timestamp int64) (time.Time, error) {
		if timestamp == 1 {
			return start, nil
		}
		return boundary, nil
	})
	for _, chunk := range []Chunk{{Format: old, Timestamp: 1, WireBytes: 1933, Samples: make([]float32, 960)}, {Format: next, Timestamp: 2, WireBytes: 7693, Samples: make([]float32, 1920*2)}} {
		value := float32(0.25)
		if chunk.Timestamp == 2 {
			value = -0.5
		}
		for i := range chunk.Samples {
			chunk.Samples[i] = value
		}
		if err := engine.Enqueue(chunk); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()
	select {
	case <-output.finished:
	case <-time.After(2 * time.Second):
		cancel()
		<-done // Cleanup: preserve the timeout failure after joining the worker.
		t.Fatal("format transition did not complete")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	output.mu.Lock()
	defer output.mu.Unlock()
	if len(output.opened) != 2 || output.opened[0] != old || output.opened[1] != next || output.flushed || output.closedAt.Before(boundary) {
		t.Fatalf("old format was not drained: opened=%v flushed=%v close=%s boundary=%s", output.opened, output.flushed, output.closedAt, boundary)
	}
	for i, write := range output.writes {
		value := float32(0.25)
		count := 960
		if i == 1 {
			value = -0.5
			count = 3840
		}
		if len(write.Samples) != count {
			t.Fatalf("write %d lost samples: %d", i, len(write.Samples))
		}
		for _, sample := range write.Samples {
			if sample != value {
				t.Fatalf("write %d reinterpreted old samples", i)
			}
		}
	}
}
