//go:build linux && (amd64 || arm64)

// Package pipewire provides direct Linux PipeWire playback through libpipewire.
// Import/configuration is inert; Open connects an inactive playback stream.
// PipeWire graph/resampler/queue delay drives timing, not wall-clock sleeps.
package pipewire

import (
	"context"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	log "github.com/echocat/slf4g"
)

type Output struct {
	// SharedLibPath, when nonempty, selects exactly this library file. A load or
	// symbol error is returned without trying the system default.
	SharedLibPath                                   string
	Device                                          string
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

func (o Output) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	return render.Open(ctx, render.Config{Backend: "pipewire", Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}, f, func(d *render.Device) (render.Driver, error) { return openNative(o, d) })
}
