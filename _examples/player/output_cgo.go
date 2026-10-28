//go:build cgo

package main

import (
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/portaudio"
)

func newPortaudioOutput() (audio.Output, error) { return portaudio.Output{}, nil }
