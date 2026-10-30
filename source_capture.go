package sendspin

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

// sourceCapture never writes to the connection or playback engine. The session
// actor owns authorization and wire ordering. One worker at a time also bounds
// late Open/Close operations; a replacement waits for the previous worker.
type sourceCapture struct {
	cancel context.CancelFunc
	ready  chan struct{}
	done   chan struct{}
	wake   chan struct{}
	mu     sync.Mutex
	queue  []sourceChunk
	err    error // read only after done
}

type sourceChunk struct {
	first    time.Time // ADC time, including any fixed native input pipeline latency.
	queuedAt time.Time // App enqueue time; independent of the ADC clock/latency.
	pcm      []byte
}

func newSourceCapture(parent context.Context, input audio.Input, format audio.Format, wake chan struct{}) *sourceCapture {
	ctx, cancel := context.WithCancel(parent)
	c := &sourceCapture{cancel: cancel, ready: make(chan struct{}), done: make(chan struct{}), wake: wake}
	go func() {
		c.err = c.run(ctx, input, format)
		cancel() // Release the parent's child registration even after Open failure.
		close(c.done)
		c.notify()
	}()
	return c
}

func (c *sourceCapture) notify() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *sourceCapture) push(chunk sourceChunk) {
	c.mu.Lock()
	chunk.queuedAt = monotime.Now()
	// At most 60 ms of 20-ms blocks. Prefer live capture to a stale burst.
	if len(c.queue) == 3 {
		copy(c.queue, c.queue[1:])
		c.queue = c.queue[:2]
	}
	c.queue = append(c.queue, chunk)
	c.mu.Unlock()
	c.notify()
}

func (c *sourceCapture) pop() (sourceChunk, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return sourceChunk{}, false
	}
	chunk := c.queue[0]
	copy(c.queue, c.queue[1:])
	c.queue[len(c.queue)-1] = sourceChunk{}
	c.queue = c.queue[:len(c.queue)-1]
	if len(c.queue) > 0 {
		c.notify()
	}
	return chunk, true
}

func (c *sourceCapture) run(ctx context.Context, input audio.Input, f audio.Format) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := input.Open(ctx, f)
	if d != nil {
		defer func() {
			if closeErr := d.Close(); err == nil {
				err = closeErr
			}
		}()
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if d == nil {
		return errors.New("sendspin: input returned no device")
	}
	close(c.ready)
	c.notify()
	frames := f.SampleRate / 50
	samples := frames * f.Channels
	buffer := make([]float32, samples)
	block := make([]float32, 0, samples)
	var first time.Time
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, at, readErr := d.Read(ctx, buffer[:samples-len(block)])
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n < 0 || n > samples-len(block) || n%f.Channels != 0 || (n > 0 && at.IsZero()) {
			return errors.New("sendspin: invalid input read")
		}
		var discontinuity audio.InputDiscontinuityError
		reset := errors.As(readErr, &discontinuity) && discontinuity.AudioInputDiscontinuity()
		if reset {
			block = block[:0]
		}
		if n > 0 {
			emptyReads = 0
			if len(block) > 0 {
				expected := first.Add(time.Duration(len(block)/f.Channels) * time.Second / time.Duration(f.SampleRate))
				if delta := at.Sub(expected); delta > 5*time.Millisecond || delta < -5*time.Millisecond {
					block = block[:0]
				}
			}
			if len(block) == 0 {
				first = at
			}
			block = append(block, buffer[:n]...)
		} else {
			emptyReads++
			if emptyReads >= 16 {
				return io.ErrNoProgress
			}
		}
		if len(block) == samples || (readErr != nil && !reset && len(block) > 0) {
			pcm := make([]byte, len(block)*2)
			for i, sample := range block {
				v := float64(sample)
				if math.IsNaN(v) {
					v = 0
				}
				value := int16(max(-32768, min(32767, math.Round(v*32768))))
				pcm[2*i], pcm[2*i+1] = byte(value), byte(uint16(value)>>8)
			}
			c.push(sourceChunk{first: first, pcm: pcm})
			block = block[:0]
		}
		if readErr != nil && !reset {
			return readErr
		}
	}
}
