//go:build e2e && darwin && (amd64 || arm64)

package coreaudio

import (
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
)

func TestReferenceServerPlaybackScenario(t *testing.T) {
	audiotest.RunReferenceServerPlaybackScenario(t, audiotest.ScenarioConfig{
		Prefix:        "SENDSPIN_COREAUDIO",
		OutputFactory: func(device string) audio.Output { return Output{Device: device} },
	})
}
