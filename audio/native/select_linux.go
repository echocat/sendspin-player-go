//go:build linux && (amd64 || arm64)

package native

import (
	"context"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/alsa"
	"github.com/echocat/sendspin-player-go/audio/pipewire"
)

func (o Output) openPlatform(ctx context.Context, f audio.Format) (audio.Device, error) {
	openPW := func() (audio.Device, error) {
		return (pipewire.Output{Device: o.Device, SharedLibPath: o.SharedLibPath, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	}
	openALSA := func() (audio.Device, error) {
		name := o.Device
		if name == "" {
			name = "hw"
		}
		return (alsa.Output{Device: name, SharedLibPath: o.SharedLibPath, BufferDuration: o.BufferDuration, PeriodDuration: o.PeriodDuration, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	}
	switch o.Backend {
	case BackendPipeWire:
		d, err := openPW()
		return outputResult(d, BackendPipeWire, err)
	case BackendALSA:
		d, err := openALSA()
		return outputResult(d, BackendALSA, err)
	case BackendAuto:
		d, backend, err := autoSelect(ctx, o.SharedLibPath != "", openPW, openALSA)
		return outputResult(d, backend, err)
	default:
		return nil, ErrUnavailable
	}
}
func (o Input) openPlatform(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	openPW := func() (audio.InputDevice, error) {
		return (pipewire.Input{Device: o.Device, SharedLibPath: o.SharedLibPath, Loopback: o.Loopback, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	}
	openALSA := func() (audio.InputDevice, error) {
		if o.Loopback {
			return nil, ErrUnavailable
		}
		name := o.Device
		if name == "" {
			name = "hw"
		}
		return (alsa.Input{Device: name, SharedLibPath: o.SharedLibPath, BufferDuration: o.BufferDuration, PeriodDuration: o.PeriodDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}).Open(ctx, f)
	}
	switch o.Backend {
	case BackendPipeWire:
		d, err := openPW()
		return inputResult(d, BackendPipeWire, err)
	case BackendALSA:
		d, err := openALSA()
		return inputResult(d, BackendALSA, err)
	case BackendAuto:
		d, backend, err := autoSelect(ctx, o.SharedLibPath != "", openPW, openALSA)
		return inputResult(d, backend, err)
	default:
		return nil, ErrUnavailable
	}
}
