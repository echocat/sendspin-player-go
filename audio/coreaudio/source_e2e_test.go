//go:build e2e && darwin && (amd64 || arm64)

package coreaudio

import (
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
)

func TestReferenceServerNativeInputScenario(t *testing.T) {
	audiotest.RunReferenceServerNativeInputScenario(t, audiotest.SourceScenarioConfig{
		Prefix:       "SENDSPIN_COREAUDIO",
		InputFactory: func(device string) audio.Input { return Input{Device: device} },
	})
}
