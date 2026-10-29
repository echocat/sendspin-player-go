//go:build darwin && (amd64 || arm64)

package native

import (
	"context"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/coreaudio"
)

func (o Output) openPlatform(ctx context.Context, f audio.Format) (audio.Device, error) {
	if o.Backend != BackendAuto && o.Backend != BackendCoreAudio {
		return nil, ErrUnavailable
	}
	d, err := (coreaudio.Output{Device: o.Device, SharedLibPath: o.SharedLibPath, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	return outputResult(d, BackendCoreAudio, err)
}
func (o Input) openPlatform(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	if (o.Backend != BackendAuto && o.Backend != BackendCoreAudio) || o.Loopback {
		return nil, ErrUnavailable
	}
	d, err := (coreaudio.Input{Device: o.Device, SharedLibPath: o.SharedLibPath, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	return inputResult(d, BackendCoreAudio, err)
}
