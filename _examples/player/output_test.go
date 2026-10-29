package main

import (
	"testing"

	"github.com/echocat/sendspin-player-go/audio/native"
)

func TestOutputSelectionDoesNotInitializeAudio(t *testing.T) {
	for _, name := range []string{"native", "pipewire", "alsa", "wasapi", "coreaudio", "noop"} {
		o, err := newOutput(name, "selected-device", "")
		if err != nil {
			t.Fatal(err)
		}
		selected, ok := o.(native.Output)
		if !ok || selected.Device != "selected-device" {
			t.Fatal("selection lost device")
		}
	}
	for _, name := range []string{"unknown", "oto", "portaudio"} {
		if _, err := newOutput(name, "", ""); err == nil {
			t.Fatal("unsupported output accepted", name)
		}
	}
}
