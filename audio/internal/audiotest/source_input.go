package audiotest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

var sourceCaptureFormat = audio.Format{SampleRate: 48000, Channels: 2}

// sourceInputTap observes geometry/lifecycle only. It never retains, hashes,
// inspects or logs samples, and never retries/reopens a failed input itself.
type sourceInputTap struct {
	factory                                                            func() audio.Input
	attempts, opens, reads, closes, samples, failures, discontinuities atomic.Int64
}

type sourceInputStats struct {
	Attempts        int64 `json:"open_attempts"`
	Opens           int64 `json:"opens"`
	Reads           int64 `json:"reads"`
	Closes          int64 `json:"closes"`
	Samples         int64 `json:"samples"`
	Failures        int64 `json:"unexpected_errors"`
	Discontinuities int64 `json:"discontinuities"`
}

func (i *sourceInputTap) stats() sourceInputStats {
	return sourceInputStats{i.attempts.Load(), i.opens.Load(), i.reads.Load(), i.closes.Load(), i.samples.Load(), i.failures.Load(), i.discontinuities.Load()}
}

func (i *sourceInputTap) Open(ctx context.Context, format audio.Format) (audio.InputDevice, error) {
	i.attempts.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if format != sourceCaptureFormat {
		i.failures.Add(1)
		return nil, errors.New("unexpected source capture format")
	}
	base := i.factory()
	if base == nil {
		i.failures.Add(1)
		return nil, errors.New("source input factory returned nil; no fallback")
	}
	d, err := base.Open(ctx, format)
	if err != nil && ctx.Err() == nil {
		i.failures.Add(1)
	}
	if d == nil {
		if err == nil {
			i.failures.Add(1)
			err = errors.New("source input returned nil device")
		}
		return nil, err
	}
	i.opens.Add(1)
	// A partial Open result still belongs to the SDK worker and must be closed.
	return &sourceTapDevice{InputDevice: d, owner: i}, err
}

type sourceTapDevice struct {
	audio.InputDevice
	owner    *sourceInputTap
	once     sync.Once
	closeErr error
}

func (d *sourceTapDevice) Read(ctx context.Context, dst []float32) (int, time.Time, error) {
	d.owner.reads.Add(1)
	n, first, err := d.InputDevice.Read(ctx, dst)
	if n < 0 || n > len(dst) || n%sourceCaptureFormat.Channels != 0 || (n > 0 && first.IsZero()) {
		d.owner.failures.Add(1)
		return 0, time.Time{}, errors.New("invalid native source read geometry/timestamp")
	}
	if n > 0 {
		// Broad local-domain sanity only, NOT ADC calibration or a latency limit.
		age := time.Since(first)
		if age < -time.Second || age > 10*time.Second {
			d.owner.failures.Add(1)
			return 0, time.Time{}, errors.New("native source timestamp outside local clock-domain sanity window")
		}
		d.owner.samples.Add(int64(n))
	}
	var discontinuity audio.InputDiscontinuityError
	if errors.As(err, &discontinuity) && discontinuity.AudioInputDiscontinuity() {
		d.owner.discontinuities.Add(1)
	} else if err != nil && ctx.Err() == nil {
		d.owner.failures.Add(1)
	}
	return n, first, err
}

func (d *sourceTapDevice) Close() error {
	d.once.Do(func() {
		d.closeErr = d.InputDevice.Close()
		d.owner.closes.Add(1)
		if d.closeErr != nil {
			d.owner.failures.Add(1)
		}
	})
	return d.closeErr
}
