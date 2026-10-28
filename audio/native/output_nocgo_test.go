//go:build !cgo

package native_test

import (
	"context"
	"errors"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/native"
	"testing"
)

func TestUnavailableWithoutCGO(t *testing.T) {
	if _, err := (native.Output{}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); !errors.Is(err, native.ErrUnavailable) {
		t.Fatal(err)
	}
}
