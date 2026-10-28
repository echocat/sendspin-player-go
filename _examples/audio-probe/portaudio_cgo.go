//go:build cgo

package main

import (
	"errors"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/portaudio"
	pa "github.com/gordonklaus/portaudio"
)

func portaudioOutput() (audio.Output, []nativeDevice, string, error) {
	if err := pa.Initialize(); err != nil {
		return nil, nil, "driver DAC clock", err
	}
	devices, err := pa.Devices()
	var found []nativeDevice
	if err == nil {
		for _, d := range devices {
			if d.MaxOutputChannels > 0 {
				host := ""
				if d.HostApi != nil {
					host = d.HostApi.Name
				}
				found = append(found, nativeDevice{Name: d.Name, HostAPI: host, OutputChannels: d.MaxOutputChannels})
			}
		}
	}
	err = errors.Join(err, pa.Terminate())
	if err != nil {
		return nil, found, "driver DAC clock", err
	}
	if len(found) == 0 {
		return nil, found, "driver DAC clock", errors.New("no PortAudio output device is available")
	}
	return portaudio.Output{}, found, "driver DAC clock; not physical measurement", nil
}
