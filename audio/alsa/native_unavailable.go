//go:build !linux || !cgo

package alsa

import (
	"context"
	"github.com/echocat/sendspin-player-go/audio"
)

func openNative(ctx context.Context, _ Output, _ audio.Format) (pcm, negotiated, error) {
	if err := ctx.Err(); err != nil {
		return nil, negotiated{}, err
	}
	return nil, negotiated{}, ErrUnavailable
}
