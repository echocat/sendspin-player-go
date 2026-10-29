// Package playback implements the shared, hardware-independent render worker.
package playback

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

type Chunk struct {
	Format    audio.Format
	Timestamp int64
	Samples   []float32
	WireBytes int
}

// Engine is safe for a network producer and one render worker. Queue reset
// invalidates an in-flight Write through its context, then flushes on the worker.
type Engine struct {
	mu                                sync.Mutex
	queue                             []Chunk
	bytes, samples                    int
	Capacity                          int
	DecodedLimit                      int
	output                            audio.Output
	localTime                         func(int64) (time.Time, error)
	wake                              chan struct{}
	generation                        uint64
	cancel                            context.CancelFunc
	volume                            int
	muted                             bool
	delay                             int
	stateChanged                      chan struct{}
	corrections                       correctionWindow // Render-worker owned, across resets/formats.
	peakWire, peakDecoded, peakChunks int
}

// BufferStats is an internal, synchronized diagnostic snapshot. It describes
// queued software ownership/accounting, excluding the popped/in-flight chunk
// and backend buffers. It never establishes hardware latency or audible output.
type BufferStats struct {
	Chunks, WireBytes, DecodedBytes, PeakChunks, PeakWireBytes, PeakDecodedBytes int
	Generation                                                                   uint64
}

func (e *Engine) Stats() BufferStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return BufferStats{Chunks: len(e.queue), WireBytes: e.bytes, DecodedBytes: e.samples, PeakChunks: e.peakChunks, PeakWireBytes: e.peakWire, PeakDecodedBytes: e.peakDecoded, Generation: e.generation}
}

func New(output audio.Output, capacity int, localTime func(int64) (time.Time, error)) *Engine {
	return &Engine{output: output, Capacity: capacity, DecodedLimit: 32 << 20, localTime: localTime, wake: make(chan struct{}, 1), stateChanged: make(chan struct{}), volume: 100}
}
func (e *Engine) notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) Enqueue(c Chunk) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Account both encoded bytes and decoded samples. Oversized individual
	// chunks are rejected; temporary aggregate excess triggers bounded recovery.
	if c.WireBytes > e.Capacity || len(c.Samples)*4 > e.DecodedLimit {
		return errors.New("sendspin: playback buffer limit")
	}
	if e.bytes > e.Capacity-c.WireBytes || len(c.Samples)*4 > e.DecodedLimit-e.samples {
		// A decreased output delay can temporarily exceed the advertised cap.
		// Remain operational: discard buffered audio and recover at the new
		// timeline instead of treating that permitted excess as a disconnect.
		e.queue = nil
		e.bytes = 0
		e.samples = 0
		e.generation++
		if e.cancel != nil {
			e.cancel()
		}
	}
	e.queue = append(e.queue, c)
	e.bytes += c.WireBytes
	e.samples += len(c.Samples) * 4
	e.peakWire = max(e.peakWire, e.bytes)
	e.peakDecoded = max(e.peakDecoded, e.samples)
	e.peakChunks = max(e.peakChunks, len(e.queue))
	e.notify()
	return nil
}

func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.queue = nil
	e.bytes = 0
	e.samples = 0
	e.generation++
	if e.cancel != nil {
		e.cancel()
	}
	e.notify()
}
func (e *Engine) State(volume int, muted bool, delay int) {
	e.mu.Lock()
	if e.volume != volume || e.muted != muted || e.delay != delay {
		close(e.stateChanged)
		e.stateChanged = make(chan struct{})
	}
	e.volume, e.muted, e.delay = volume, muted, delay
	e.mu.Unlock()
}

func (e *Engine) Run(ctx context.Context) error {
	var device audio.Device
	var format audio.Format
	var generation uint64
	gain := float32(1)
	defer func() {
		if device != nil {
			_ = device.Close()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.mu.Lock()
		reset := generation != e.generation
		generation = e.generation
		var chunk Chunk
		if len(e.queue) > 0 {
			chunk = e.queue[0]
			e.queue[0] = Chunk{}
			e.queue = e.queue[1:]
			e.bytes -= chunk.WireBytes
			e.samples -= len(chunk.Samples) * 4
		}
		work, cancel := context.WithCancel(ctx)
		e.cancel = cancel
		e.mu.Unlock()
		if reset && device != nil {
			if err := device.Flush(ctx); err != nil {
				cancel()
				return err
			}
		}
		if work.Err() != nil {
			cancel()
			continue
		}
		if chunk.Samples == nil {
			cancel()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-e.wake:
				continue
			}
		}
		if device == nil || format != chunk.Format {
			if device != nil {
				// Write may return while hardware is still playing its queue.
				// Finish the previous format rather than dropping its tail.
				next, err := device.Next(work)
				if err != nil {
					interrupted := work.Err() != nil
					cancel()
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if interrupted && errors.Is(err, context.Canceled) {
						continue
					}
					return err
				}
				for next.After(monotime.Now()) && work.Err() == nil {
					timer := time.NewTimer(monotime.Until(next))
					select {
					case <-work.Done():
						timer.Stop()
					case <-timer.C:
					}
				}
				// Reset invalidates the popped chunk as well as the queue. Keep
				// this device for the next generation's Flush, not an obsolete Open.
				if work.Err() != nil {
					cancel()
					continue
				}
				old := device
				device = nil // Close is attempted exactly once, even on failure.
				if err := old.Close(); err != nil {
					cancel()
					return err
				}
			}
			if work.Err() != nil {
				cancel()
				continue
			}
			var err error
			device, err = e.output.Open(work, chunk.Format)
			if err != nil {
				interrupted := work.Err() != nil
				cancel()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if interrupted && errors.Is(err, context.Canceled) {
					continue
				}
				return err
			}
			format = chunk.Format
		}
		if work.Err() != nil {
			cancel()
			continue
		}
		err := e.renderRecovering(work, device, chunk, &gain)
		interrupted := work.Err() != nil
		cancel()
		if err != nil && !(interrupted && errors.Is(err, context.Canceled)) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (e *Engine) renderRecovering(ctx context.Context, device audio.Device, c Chunk, gain *float32) error {
	var err error
	for range 4 {
		if err := ctx.Err(); err != nil {
			return err
		}
		previous := *gain
		err = e.render(ctx, device, c, gain)
		var reset audio.TimelineResetError
		if !errors.As(err, &reset) {
			return err
		}
		if reset.AudioTimelineReset() {
			if flushErr := device.Flush(ctx); flushErr != nil {
				return flushErr
			}
			*gain = 0
		} else {
			*gain = previous
		}
		// Remap from the original source timestamp. render discards stale frames;
		// never replay a partial submission or change production retry/backoff.
	}
	return err
}

func (e *Engine) render(ctx context.Context, device audio.Device, c Chunk, gain *float32) error {
	var target, next time.Time
	var volume int
	var muted bool
	// Do not fill the device with seconds of startup silence. Remap the target
	// after waiting: the time filter can improve while the chunk is queued.
	for {
		var err error
		next, err = device.Next(ctx)
		if err != nil {
			return err
		}
		e.mu.Lock()
		delay, changed := e.delay, e.stateChanged
		volume, muted = e.volume, e.muted
		e.mu.Unlock()
		// Output delay is clamped to 0..5000 ms at the protocol boundary.
		// Saturate an unrepresentable earlier timestamp, never wrap to future.
		timestamp := c.Timestamp
		offset := int64(delay) * 1000
		if timestamp < math.MinInt64+offset {
			timestamp = math.MinInt64
		} else {
			timestamp -= offset
		}
		target, err = e.localTime(timestamp)
		if err != nil {
			return err
		}
		delta := target.Sub(next)
		if delta <= 150*time.Millisecond {
			break
		}
		// Also periodically remap the time filter; a centuries-ahead timestamp
		// must not postpone clock-model updates for centuries.
		timer := time.NewTimer(min(delta-100*time.Millisecond, 100*time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		case <-changed:
			timer.Stop()
		}
	}
	delta := target.Sub(next)
	samples := c.Samples
	channels, rate := c.Format.Channels, c.Format.SampleRate
	frames := len(samples) / channels
	if delta < -time.Millisecond {
		// One-shot recovery, not accelerated playback of stale audio.
		// Compute a positive saturated difference instead of negating delta:
		// -MinInt64 would wrap. Compare before conversion to int so even a
		// centuries-old target is safe on 32-bit platforms.
		lateFrames := next.Sub(target).Seconds() * float64(rate)
		drop := frames
		if lateFrames < float64(frames) {
			drop = int(lateFrames)
		}
		samples = samples[drop*channels:]
	} else if delta > time.Millisecond {
		count := int(delta.Seconds() * float64(rate))
		if err := device.Write(ctx, make([]float32, count*channels)); err != nil {
			return err
		}
	} else if delta > 100*time.Microsecond || delta < -100*time.Microsecond {
		// Keep the suggested small step/per-chunk cap, but additionally bound
		// absolute corrections across every 150-ms output window, not merely
		// against each input chunk's duration.
		n := min(max(1, int(math.Round(0.000021*float64(rate)))), frames/200)
		n = e.corrections.allow(next, rate, n)
		if n > 0 && delta < 0 {
			samples = samples[n*channels:]
		} else if n > 0 {
			prefix := make([]float32, n*channels)
			for i := range n {
				copy(prefix[i*channels:], samples[:channels])
			}
			samples = append(prefix, samples...)
		}
	}
	desired := float32(math.Pow(float64(volume)/100, 1.5))
	if muted {
		desired = 0
	}
	// A 5ms ramp avoids steps without modifying caller-owned decoded evidence.
	out := append([]float32(nil), samples...)
	ramp := max(1, rate/200)
	start := *gain
	for i := 0; i < len(out)/channels; i++ {
		g := desired
		if i < ramp {
			g = start + (desired-start)*float32(i+1)/float32(ramp)
		}
		for ch := range channels {
			out[i*channels+ch] *= g
		}
	}
	if len(out) > 0 {
		if frames := len(out) / channels; frames < ramp {
			*gain = start + (desired-start)*float32(frames)/float32(ramp)
		} else {
			*gain = desired
		}
		return device.Write(ctx, out)
	}
	return nil
}
