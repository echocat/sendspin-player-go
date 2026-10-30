//go:build !linux || (!amd64 && !arm64)

package alsa

import (
	"context"
	"errors"
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestUnavailableNativeBackend(t *testing.T) {
	if _, err := (Output{}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
