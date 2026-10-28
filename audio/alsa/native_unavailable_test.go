//go:build !linux || !cgo

package alsa

import (
	"context"
	"errors"
	"github.com/echocat/sendspin-player-go/audio"
	"testing"
)

func TestUnavailableNativeBackend(t *testing.T) {
	if _, err := (Output{}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
