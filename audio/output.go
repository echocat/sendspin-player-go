// Package audio defines the contract between the shared player and audio hardware.
// Implementations are optional: importing the SDK does not load native libraries.
package audio

import (
	"context"
	"time"
)

// Format describes interleaved float32 samples in [-1,1].
type Format struct{ SampleRate, Channels int }

// Output creates a device. Open is called by one playback worker, never from the
// network loop. A device is reused across codec/bit-depth changes at the same rate.
type Output interface {
	Open(context.Context, Format) (Device, error)
}

// Device is exclusively owned by one worker. Every operation must honor context
// cancellation promptly. Write must copy samples it retains after returning.
// A backend may offer an explicitly enabled best-effort mode with weaker timing
// and downstream-flush guarantees. Such a mode must document these exceptions;
// it is not suitable evidence of physical Sendspin synchronization conformance.
//
// Next reports when the first frame of the next Write will exit the audio port,
// including already queued audio and hardware latency, in Go's monotonic time
// domain. It must track the real device clock, not a sample counter driven by
// wall-clock sleeps. Accurate feedback is necessary for Sendspin synchronization.
// Flush discards queued samples and stops the previous timeline; Close releases
// the device. Neither may leave samples playing after it returns successfully.
type Device interface {
	Next(context.Context) (time.Time, error)
	Write(context.Context, []float32) error
	Flush(context.Context) error
	Close() error
}
