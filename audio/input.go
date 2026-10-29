package audio

import (
	"context"
	"time"
)

// Input opens an explicitly selected capture device. Importing or configuring an
// Input must not initialize hardware. Open and Read must honor cancellation
// promptly. Any non-nil device returned, including with an error or after
// cancellation, is owned by the caller and will be closed by it.
// Implementations do not change input gains, global defaults or monitoring routes.
type Input interface {
	Open(context.Context, Format) (InputDevice, error)
}

// InputDevice provides interleaved float32 samples in [-1,1]. Read borrows dst
// only until it returns. n counts samples (not frames) and must be a channel
// multiple. first is the best-effort ADC time of dst[0] in the local Go monotonic
// clock domain, not the Read arrival time. Backends must bound capture queues and
// report lost capture continuity with InputDiscontinuityError. Close releases the
// device; the owning worker calls it after the final Open/Read has returned,
// never concurrently with Open/Read.
type InputDevice interface {
	Read(context.Context, []float32) (n int, first time.Time, err error)
	Close() error
}

// InputDiscontinuityError reports a dropped/invalidated capture timeline. Samples
// returned with this error, if any, begin the new timeline at first. Consumers
// discard partial old blocks; no implicit reopening or recording is authorized.
type InputDiscontinuityError interface {
	error
	AudioInputDiscontinuity() bool
}
