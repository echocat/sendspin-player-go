//go:build e2e && linux && (amd64 || arm64)

package pipewire

import (
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
	"testing"
)

func TestNativeLoopback(t *testing.T) {
	audiotest.RunNativeLoopback(t, audiotest.LoopbackConfig{
		Prefix:        "SENDSPIN_PIPEWIRE",
		OutputFactory: func(device string) audio.Output { return Output{Device: device} },
		InputFactory:  func(device string) audio.Input { return Input{Device: device, Loopback: true} },
	})
}
