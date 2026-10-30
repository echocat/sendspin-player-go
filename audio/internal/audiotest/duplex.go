package audiotest

import (
	"context"
	"errors"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

type DuplexRecorder interface {
	Start() error
	Wait() error
	Stop()
}

// DuplexLoop joins both workers before returning. The output must be ready
// before capture starts; callers provide fakes in ordinary unit tests.
func DuplexLoop(ctx context.Context, open func(context.Context, audio.Format) (audio.Device, error), recorder DuplexRecorder) (frames int64, err error) {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	if err = work.Err(); err != nil {
		return 0, err
	}
	d, err := open(work, audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		if d != nil {
			err = errors.Join(err, d.Close())
		}
		return 0, err
	}
	if d == nil {
		return 0, errors.New("duplex output returned nil device")
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	if _, err = d.Next(work); err != nil {
		return 0, err
	}
	writerCtx, stopWriter := context.WithCancel(work)
	defer stopWriter()
	type written struct {
		frames int64
		err    error
	}
	writes := make(chan written, 1)
	go func() {
		var total int64
		var writeErr error
		defer func() { writes <- written{total, writeErr} }()
		zeros := make([]float32, 960)
		for {
			if writeErr = writerCtx.Err(); writeErr != nil {
				return
			}
			if _, writeErr = d.Next(writerCtx); writeErr != nil {
				return
			}
			if writeErr = d.Write(writerCtx, zeros); writeErr != nil {
				return
			}
			total += 480
		}
	}()
	if err = recorder.Start(); err != nil {
		stopWriter()
		w := <-writes
		return w.frames, err
	}
	defer recorder.Stop()
	recorded := make(chan error, 1)
	go func() { recorded <- recorder.Wait() }()
	var w written
	select {
	case err = <-recorded:
		stopWriter()
		w = <-writes
		if w.err != nil && !errors.Is(w.err, context.Canceled) {
			err = errors.Join(err, w.err)
		}
	case w = <-writes:
		err = w.err
		recorder.Stop()
		cancel()
		err = errors.Join(err, <-recorded)
	case <-ctx.Done():
		err = ctx.Err()
		recorder.Stop()
		cancel()
		stopWriter()
		w = <-writes
		err = errors.Join(err, <-recorded)
	}
	return w.frames, err
}

// DuplexCapture reads only through the Go SDK Input path. It is not an
// independent recorder and retains no samples. Inspect counts after Wait.
type DuplexCapture struct {
	ctx     context.Context
	factory func() audio.Input
	cancel  context.CancelFunc
	done    chan error
	opened  bool
	samples int64
}

func NewDuplexCapture(ctx context.Context, factory func() audio.Input) *DuplexCapture {
	return &DuplexCapture{ctx: ctx, factory: factory}
}

func (r *DuplexCapture) Start() error {
	ctx, cancel := context.WithCancel(r.ctx)
	r.cancel = cancel
	input := r.factory()
	if input == nil {
		cancel()
		return errors.New("duplex input factory returned nil; no fallback")
	}
	d, err := input.Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		cancel()
		if d != nil {
			err = errors.Join(err, d.Close())
		}
		return err
	}
	if d == nil {
		cancel()
		return errors.New("duplex input returned nil device")
	}
	r.opened = true
	r.done = make(chan error, 1)
	go func() {
		var readErr error
		defer func() { r.done <- errors.Join(readErr, d.Close()) }()
		buf := make([]float32, 960)
		for r.samples < 10*48000*2 {
			dst := buf[:min(int64(len(buf)), 10*48000*2-r.samples)]
			n, first, err := d.Read(ctx, dst)
			if n <= 0 || n > len(dst) || n%2 != 0 || first.IsZero() {
				readErr = errors.Join(err, errors.New("invalid Go SDK input sample geometry/timestamp"))
				return
			}
			// Count interleaved samples and preserve the native monotonic clock.
			// This broad domain sanity check is not ADC/latency calibration.
			if age := time.Since(first); age < -time.Second || age > 10*time.Second {
				readErr = errors.New("capture timestamp outside local clock domain")
				return
			}
			r.samples += int64(n)
			clear(buf)
			if err != nil {
				readErr = err
				return
			}
		}
	}()
	return nil
}

func (r *DuplexCapture) Wait() error    { return <-r.done }
func (r *DuplexCapture) Stop()          { r.cancel() }
func (r *DuplexCapture) Opened() bool   { return r.opened }
func (r *DuplexCapture) Samples() int64 { return r.samples }
