package main

import (
	"fmt"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/oto"
)

func newOutput(name string, approximate bool) (audio.Output, error) {
	switch name {
	case "oto":
		return oto.Output{AllowApproximate: approximate}, nil
	case "portaudio":
		return newPortaudioOutput()
	default:
		return nil, fmt.Errorf("unknown audio output %q", name)
	}
}
