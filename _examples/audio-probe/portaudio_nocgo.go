//go:build !cgo

package main

import (
	"errors"

	"github.com/echocat/sendspin-player-go/audio"
)

func portaudioOutput() (audio.Output, []nativeDevice, string, error) {
	return nil, nil, "unavailable", errors.New("PortAudio requires a native CGO toolchain; this binary was built without CGO")
}
