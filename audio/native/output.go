// Package native selects direct PipeWire (Linux), WASAPI (Windows), or CoreAudio
// (macOS) playback. It never falls back to Oto/PortAudio or opens capture. This is
// an optional independent CGO module; the SDK core remains pure Go.
package native

import (
	"context"
	"runtime"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/native/coreaudio"
	"github.com/echocat/sendspin-player-go/audio/native/internal/render"
	"github.com/echocat/sendspin-player-go/audio/native/pipewire"
	"github.com/echocat/sendspin-player-go/audio/native/wasapi"
	log "github.com/echocat/slf4g"
)

var ErrUnavailable = render.ErrUnavailable
var ErrTiming = render.ErrTiming
var ErrClockReset = render.ErrClockReset
var ErrDriver = render.ErrDriver
var ErrClosed = render.ErrClosed

// Output selects the platform's direct backend, without changing system routing
// or volume. Device is a PipeWire node name/serial, Windows endpoint ID, or macOS
// output-device UID. Empty selects the native default at Open (no auto-migration).
// BufferDuration bounds the SDK queue, not measured hardware latency.
type Output struct {
	Device                                          string
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

func (o Output) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	switch runtime.GOOS {
	case "linux":
		return (pipewire.Output{Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	case "windows":
		return (wasapi.Output{Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	case "darwin":
		return (coreaudio.Output{Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	default:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrUnavailable
	}
}
