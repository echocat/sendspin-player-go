//go:build windows && (amd64 || arm64)

package native

import (
	"context"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/wasapi"
)

func (o Output) openPlatform(ctx context.Context, f audio.Format) (audio.Device, error) {
	if o.Backend != BackendAuto && o.Backend != BackendWASAPI {
		return nil, ErrUnavailable
	}
	d, err := (wasapi.Output{Device: o.Device, SharedLibPath: o.SharedLibPath, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	return outputResult(d, BackendWASAPI, err)
}
func (o Input) openPlatform(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	if o.Backend != BackendAuto && o.Backend != BackendWASAPI {
		return nil, ErrUnavailable
	}
	d, err := (wasapi.Input{Device: o.Device, SharedLibPath: o.SharedLibPath, Loopback: o.Loopback, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	return inputResult(d, BackendWASAPI, err)
}
