// Package coreaudio provides direct macOS AUHAL playback. Capture stays disabled;
// the unit's client format is configured without changing the device's nominal
// rate, global default, mixer or volume. Timing uses native output host timestamps.
package coreaudio

import (
	"context"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/native/internal/render"
	log "github.com/echocat/slf4g"
	"time"
)

type Output struct {
	Device                                          string
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

func (o Output) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	return render.Open(ctx, render.Config{Backend: "coreaudio", Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}, f, func(d *render.Device) (render.Driver, error) { return openNative(o, d) })
}
