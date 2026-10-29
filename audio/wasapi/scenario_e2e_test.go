//go:build e2e && windows && (amd64 || arm64)

package wasapi

import (
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
	"testing"
)

func TestReferenceServerPlaybackScenario(t *testing.T) {
	audiotest.RunReferenceServerPlaybackScenario(t, audiotest.ScenarioConfig{
		Prefix:        "SENDSPIN_WASAPI",
		OutputFactory: func(device string) audio.Output { return Output{Device: device} },
	})
}
