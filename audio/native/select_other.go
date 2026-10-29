//go:build (!linux && !windows && !darwin) || (!amd64 && !arm64)

package native

import (
	"context"
	"github.com/echocat/sendspin-player-go/audio"
)

func (Output) openPlatform(context.Context, audio.Format) (audio.Device, error) {
	return nil, ErrUnavailable
}
func (Input) openPlatform(context.Context, audio.Format) (audio.InputDevice, error) {
	return nil, ErrUnavailable
}
