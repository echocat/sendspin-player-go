//go:build !cgo

package main

import (
	"errors"

	"github.com/echocat/sendspin-player-go/audio"
)

func newPortaudioOutput() (audio.Output, error) {
	return nil, errors.New("PortAudio requires CGO and native PortAudio libraries; this executable was built without CGO")
}
