//go:build windows && (amd64 || arm64)

package wasapi

import (
	"context"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	log "github.com/echocat/slf4g"
)

// Input captures an endpoint without changing its gain, mix format or defaults.
// Device is an IMMDevice endpoint ID; empty selects the multimedia capture
// endpoint. Loopback must be explicitly enabled to capture a render endpoint
// instead (Device then identifies that render endpoint). Open is inactive;
// the first Read starts capture.
type Input struct {
	SharedLibPath                                   string
	Device                                          string
	Loopback                                        bool
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

func (i Input) Open(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	return capture.Open(ctx, capture.Config{Backend: "wasapi", Device: i.Device, BufferDuration: i.BufferDuration, StartupTimeout: i.StartupTimeout, MaxTimingWindow: i.MaxTimingWindow, Logger: i.Logger}, f, func(d *capture.Device) (capture.Driver, error) {
		p, err := openDriver(i.SharedLibPath, i.Device, true, i.Loopback, d)
		if err != nil {
			return nil, err
		}
		return p, nil
	})
}
