package main

import (
	"fmt"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/native"
)

func newOutput(name, device, sharedLib string) (audio.Output, error) {
	backend := native.Backend(name)
	if name == "native" {
		backend = native.BackendAuto
	}
	switch backend {
	case native.BackendAuto, native.BackendPipeWire, native.BackendALSA, native.BackendWASAPI, native.BackendCoreAudio, native.BackendNoop:
		return native.Output{Backend: backend, Device: device, SharedLibPath: sharedLib}, nil
	default:
		return nil, fmt.Errorf("unknown audio output %q", name)
	}
}
