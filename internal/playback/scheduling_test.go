package playback

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestOutputDelaySubtractionCannotWrapIntoTheFuture(t *testing.T) {
	for _, test := range []struct {
		timestamp int64
		delay     int
		want      int64
	}{
		{2_000_000, 123, 1_877_000}, {math.MinInt64, 5000, math.MinInt64}, {math.MinInt64 + 10, 1, math.MinInt64}, {math.MaxInt64, 5000, math.MaxInt64 - 5_000_000},
	} {
		at := time.Unix(1700000000, 0)
		var mapped int64
		engine := New(nil, 65535, func(timestamp int64) (time.Time, error) { mapped = timestamp; return at, nil })
		engine.State(100, false, test.delay)
		device := &testDevice{next: at}
		gain := float32(1)
		if err := engine.render(context.Background(), device, Chunk{Timestamp: test.timestamp, Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: make([]float32, 960)}, &gain); err != nil {
			t.Fatal(err)
		}
		if mapped != test.want {
			t.Fatal("output delay wrapped the mapped timestamp", test.timestamp, test.delay, mapped, test.want)
		}
	}
}

func TestStartupWaitWakesOnStateChangeAndMappingFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "updated-delay", true: "mapping-failed"}[fail], func(t *testing.T) {
			at := time.Now()
			mapped := make(chan int64, 100)
			mappingError := errors.New("no usable clock mapping")
			engine := New(nil, 65535, func(timestamp int64) (time.Time, error) {
				mapped <- timestamp
				if timestamp == 0 && fail {
					return time.Time{}, mappingError
				}
				return at.Add(time.Duration(timestamp) * time.Microsecond), nil
			})
			device := &testDevice{next: at}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				gain := float32(1)
				done <- engine.render(ctx, device, Chunk{Timestamp: 5_000_000, Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: make([]float32, 960)}, &gain)
			}()
			select {
			case <-mapped:
			case <-ctx.Done():
				t.Fatal("startup mapping not attempted")
			}
			engine.State(100, false, 5000)
			err := <-done
			if fail {
				if !errors.Is(err, mappingError) || len(device.writes) != 0 {
					t.Fatal("mapping error allowed output or was swallowed", err)
				}
			} else if err != nil || len(device.writes) != 1 || len(device.writes[0]) != 960 {
				t.Fatal("updated startup delay was ignored", err)
			}
		})
	}
}

type timelineDevice struct {
	next    time.Time
	format  audio.Format
	written int
}

func (d *timelineDevice) Next(ctx context.Context) (time.Time, error) { return d.next, ctx.Err() }
func (d *timelineDevice) Write(ctx context.Context, samples []float32) error {
	d.written = len(samples) / d.format.Channels
	d.next = d.next.Add(time.Duration(d.written) * time.Second / time.Duration(d.format.SampleRate))
	return ctx.Err()
}
func (d *timelineDevice) Flush(context.Context) error { return nil }
func (d *timelineDevice) Close() error                { return nil }

func TestSoftCorrectionWindowIncludesRateChangesSignsAndResets(t *testing.T) {
	device := &timelineDevice{next: time.Unix(1700000000, 0)}
	var direction int
	engine := New(nil, 1<<20, func(int64) (time.Time, error) {
		return device.next.Add(time.Duration(direction) * 500 * time.Microsecond), nil
	})
	type event struct {
		start, end time.Time
		ticks      int64
	}
	var events []event
	const clock = 56_448_000        // Exact common multiple of every rate below.
	const budget = clock * 3 / 4000 // 0.5% of 150 ms in exact rational ticks.
	rates := []int{8000, 12000, 16000, 22050, 44100, 48000, 96000, 192000, 384000}
	gain := float32(1)
	for i := range 1800 {
		rate := rates[i%len(rates)]
		channels := 1 + i%8
		device.format = audio.Format{SampleRate: rate, Channels: channels}
		frames := 200 + (i%7)*113
		direction = 1
		if i%3 == 0 {
			direction = -1
		}
		next := device.next
		if i%11 == 0 {
			engine.Reset()
		}
		if err := engine.render(context.Background(), device, Chunk{Format: device.format, Samples: make([]float32, frames*channels)}, &gain); err != nil {
			t.Fatal(err)
		}
		delta := device.written - frames
		if delta != 0 {
			count := delta
			if count < 0 {
				count = -count
			}
			end := next
			if delta > 0 {
				end = end.Add((time.Duration(count)*time.Second + time.Duration(rate) - 1) / time.Duration(rate))
			}
			events = append(events, event{start: next, end: end, ticks: int64(count) * (clock / int64(rate))})
		}
		if len(engine.corrections.events) > 288 {
			t.Fatal("correction history grows with stream length")
		}
	}
	if len(events) < 100 {
		t.Fatal("window evidence accidentally exercised no corrections")
	}
	// A maximal window has an endpoint at a correction boundary. Count the
	// entire absolute edit duration if any part intersects; opposite signs do
	// not cancel. This deliberately overestimates fractional insert overlap.
	for _, boundary := range events {
		for _, end := range []time.Time{boundary.start, boundary.end, boundary.start.Add(150 * time.Millisecond), boundary.end.Add(150 * time.Millisecond)} {
			start := end.Add(-150 * time.Millisecond)
			var ticks int64
			for _, e := range events {
				if !e.start.After(end) && !e.end.Before(start) {
					ticks += e.ticks
				}
			}
			if ticks > budget {
				t.Fatal("150-ms window exceeded across rate/channel/sign/reset transitions", ticks, budget)
			}
		}
	}
}

func TestCorrectionBudgetCannotBeReusedAtTheExpiryBoundary(t *testing.T) {
	var window correctionWindow
	at := time.Unix(1700000000, 0)
	// Six 125-us frames consume the full 750-us budget.
	if got := window.allow(at, 8000, 6); got != 6 {
		t.Fatal(got)
	}
	for _, next := range []time.Time{at.Add(-time.Second), at.Add(150 * time.Millisecond), at.Add(150*time.Millisecond + 750*time.Microsecond)} {
		if got := window.allow(next, 384000, 1); got != 0 {
			t.Fatal("reused history before all possible overlapping windows expired", next, got)
		}
	}
	if got := window.allow(at.Add(150*time.Millisecond+750*time.Microsecond+1), 8000, 6); got != 6 {
		t.Fatal("expired correction budget never replenished", got)
	}
}

type waitingDevice struct {
	testDevice
	entered, release chan struct{}
	first            bool
}

func (d *waitingDevice) Next(ctx context.Context) (time.Time, error) {
	if !d.first {
		d.first = true
		close(d.entered)
		select {
		case <-d.release:
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		}
	}
	return d.next, ctx.Err()
}

func TestWaitingChunkUsesLatestOutputDelayAndGain(t *testing.T) {
	at := time.Unix(1700000000, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	device := &waitingDevice{testDevice: testDevice{next: at}, entered: make(chan struct{}), release: make(chan struct{})}
	var mapped int64
	engine := New(nil, 65535, func(timestamp int64) (time.Time, error) {
		mapped = timestamp
		return at.Add(time.Duration(timestamp-800000) * time.Microsecond), nil
	})
	samples := make([]float32, 960)
	for i := range samples {
		samples[i] = 1
	}
	done := make(chan error, 1)
	go func() {
		gain := float32(1)
		done <- engine.render(ctx, device, Chunk{Timestamp: 1_000_000, Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: samples}, &gain)
	}()
	<-device.entered
	engine.State(50, false, 200)
	close(device.release)
	if err := <-done; err != nil {
		t.Fatal("waiting chunk ignored live output-delay update", err)
	}
	if mapped != 800000 || len(device.writes) != 1 || len(device.writes[0]) != len(samples) {
		t.Fatal("waiting chunk used stale delay or inserted obsolete silence", mapped)
	}
	if got := device.writes[0][len(samples)-1]; got != float32(math.Pow(.5, 1.5)) {
		t.Fatal("waiting chunk used stale gain", got)
	}
}

type correctionDevice struct {
	base      time.Time
	rate      int
	frames    int64
	lastCount int
}

func (d *correctionDevice) Next(ctx context.Context) (time.Time, error) {
	return d.base.Add(time.Duration(d.frames) * time.Second / time.Duration(d.rate)), ctx.Err()
}
func (d *correctionDevice) Write(ctx context.Context, samples []float32) error {
	d.lastCount = len(samples)
	d.frames += int64(len(samples))
	return ctx.Err()
}
func (d *correctionDevice) Flush(context.Context) error { return nil }
func (d *correctionDevice) Close() error                { return nil }

func TestSoftCorrectionRespectsEvery150msOutputWindow(t *testing.T) {
	for _, rate := range []int{8000, 12000, 16000, 22050, 44100, 48000, 96000, 192000, 384000} {
		for _, direction := range []int{-1, 1} {
			device := &correctionDevice{base: time.Unix(1700000000, 0), rate: rate}
			engine := New(nil, 65535, func(int64) (time.Time, error) {
				next, _ := device.Next(context.Background())
				return next.Add(time.Duration(direction) * 500 * time.Microsecond), nil
			})
			var events []struct {
				at     time.Time
				frames int
			}
			gain := float32(1)
			for range 500 {
				next, _ := device.Next(context.Background())
				if err := engine.render(context.Background(), device, Chunk{Format: audio.Format{SampleRate: rate, Channels: 1}, Samples: make([]float32, 200)}, &gain); err != nil {
					t.Fatal(err)
				}
				correction := device.lastCount - 200
				if correction < 0 {
					correction = -correction
				}
				if correction > 0 {
					events = append(events, struct {
						at     time.Time
						frames int
					}{next, correction})
				}
				count := 0
				for _, event := range events {
					if event.at.After(next.Add(-150 * time.Millisecond)) {
						count += event.frames
					}
				}
				if count > rate*3/4000 {
					t.Fatal("per-chunk cap exceeded the 150-ms correction budget", rate, direction, count, rate*3/4000)
				}
			}
		}
	}
}
