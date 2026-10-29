//go:build e2e && windows && (amd64 || arm64)

package wasapi

import (
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
)

func TestReferenceServerNativeInputScenario(t *testing.T) {
	audiotest.RunReferenceServerNativeInputScenario(t, audiotest.SourceScenarioConfig{
		Prefix: "SENDSPIN_WASAPI",
		// This ID must name a capture endpoint. Never derive it from playback ENV
		// or enable WASAPI render-loopback for a normal Source scenario.
		InputFactory: func(device string) audio.Input { return Input{Device: device} },
	})
}
