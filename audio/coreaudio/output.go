//go:build darwin && (amd64 || arm64)

// Package coreaudio provides direct macOS AUHAL playback and capture. Each unit
// enables only its requested direction, without changing device rates, global
// defaults, mixer or volume. Timing uses native mach host timestamps.
// An unsafe native teardown quarantines the cached API until process restart:
// retained callback state stays alive, and further input/output Opens fail
// rather than leaking additional units or silently selecting another backend.
package coreaudio

import (
	"context"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	log "github.com/echocat/slf4g"
	"time"
)

type Output struct {
	SharedLibPath                                   string
	CoreAudioLibPath, CoreFoundationLibPath         string
	Device                                          string
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

// Open initializes output without changing the selected device's nominal rate.
// The client rate must already match that device rate (AUHAL has no SRC).
func (o Output) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	return render.Open(ctx, render.Config{Backend: "coreaudio", Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}, f, func(d *render.Device) (render.Driver, error) { return openNative(o, d) })
}
