// Package wasapi provides direct Windows event-driven shared-mode WASAPI output.
// It uses IAudioClock position/QPC correlation, without touching session or system
// volume. COM interfaces remain owned by one native worker thread.
package wasapi

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
	return render.Open(ctx, render.Config{Backend: "wasapi", Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}, f, func(d *render.Device) (render.Driver, error) { return openNative(o, d) })
}
