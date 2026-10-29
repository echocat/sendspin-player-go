// Package native selects native audio through compile-time platform files.
// Linux prefers usable PipeWire and otherwise tries direct ALSA. Developers can
// explicitly choose a backend. Noop is never an automatic fallback.
package native

import (
	"context"
	"errors"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"github.com/echocat/sendspin-player-go/audio/noop"
	log "github.com/echocat/slf4g"
)

type Backend string

const (
	BackendAuto      Backend = ""
	BackendPipeWire  Backend = "pipewire"
	BackendALSA      Backend = "alsa"
	BackendWASAPI    Backend = "wasapi"
	BackendCoreAudio Backend = "coreaudio"
	BackendNoop      Backend = "noop"
)

var ErrUnavailable = render.ErrUnavailable
var ErrTiming = render.ErrTiming
var ErrClockReset = render.ErrClockReset
var ErrDriver = render.ErrDriver
var ErrClosed = render.ErrClosed

type Output struct {
	Backend                                                         Backend
	Device, SharedLibPath                                           string
	BufferDuration, PeriodDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                                          log.Logger
}
type Input struct {
	Backend               Backend
	Device, SharedLibPath string
	// Loopback explicitly captures a render sink where the platform supports it.
	// It is never inferred from an output or selected for normal source capture.
	Loopback                                                        bool
	BufferDuration, PeriodDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                                          log.Logger
}
type SelectedBackend interface{ Backend() Backend }
type selectedOutput struct {
	audio.Device
	backend Backend
}

func (d *selectedOutput) Backend() Backend { return d.backend }

type selectedInput struct {
	audio.InputDevice
	backend Backend
}

func (d *selectedInput) Backend() Backend { return d.backend }
func outputResult(d audio.Device, backend Backend, err error) (audio.Device, error) {
	if err != nil {
		return nil, err
	}
	return &selectedOutput{d, backend}, nil
}
func inputResult(d audio.InputDevice, backend Backend, err error) (audio.InputDevice, error) {
	if err != nil {
		return nil, err
	}
	return &selectedInput{d, backend}, nil
}
func (o Output) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := (render.Config{Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow}).Normalize(f); err != nil {
		return nil, err
	}
	if o.Backend == BackendNoop {
		if o.SharedLibPath != "" {
			return nil, errors.New("native: noop cannot use a shared library")
		}
		d, err := (noop.Output{}).Open(ctx, f)
		return outputResult(d, BackendNoop, err)
	}
	return o.openPlatform(ctx, f)
}
func (o Input) Open(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := (render.Config{Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow}).Normalize(f); err != nil {
		return nil, err
	}
	if o.Backend == BackendNoop {
		return nil, errors.New("native: noop is output-only, never a fake capture fallback")
	}
	return o.openPlatform(ctx, f)
}
