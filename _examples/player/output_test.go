package main

import (
	"testing"

	"github.com/echocat/sendspin-player-go/audio/oto"
)

func TestOutputSelectionDoesNotInitializeAudio(t *testing.T) {
	for _, allow := range []bool{false, true} {
		output, err := newOutput("oto", allow)
		if err != nil {
			t.Fatal(err)
		}
		selected, ok := output.(oto.Output)
		if !ok || selected.AllowApproximate != allow {
			t.Fatal("selection lost explicit best-effort choice")
		}
	}
	if _, err := newOutput("unknown", false); err == nil {
		t.Fatal("accepted unknown output")
	}
	_, nativeErr := newPortaudioOutput()
	output, err := newOutput("portaudio", false)
	if (nativeErr == nil) != (err == nil) || (err == nil && output == nil) {
		t.Fatal("native selection did not follow CGO availability", err)
	}
}
