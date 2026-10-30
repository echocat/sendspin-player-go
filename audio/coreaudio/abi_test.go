//go:build darwin && (amd64 || arm64)

package coreaudio

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
)

func TestCoreAudioABILayoutAndStrictLibrary(t *testing.T) {
	if unsafe.Sizeof(streamDescription{}) != 40 || unsafe.Offsetof(streamDescription{}.Channels) != 28 || unsafe.Sizeof(audioTimestamp{}) != 64 || unsafe.Offsetof(audioTimestamp{}.HostTime) != 8 || unsafe.Offsetof(audioTimestamp{}.SMPTE) != 32 || unsafe.Offsetof(audioTimestamp{}.Flags) != 56 || unsafe.Sizeof(bufferList{}) != 24 || unsafe.Offsetof(bufferList{}.First) != 8 || unsafe.Offsetof(audioBuffer{}.Data) != 8 || unsafe.Sizeof(translation{}) != 32 || unsafe.Offsetof(translation{}.Output) != 16 || unsafe.Sizeof(callbackSpec{}) != 16 || unsafe.Sizeof(componentDescription{}) != 20 || unsafe.Sizeof(address{}) != 12 {
		t.Fatal("CoreAudio ABI layout changed")
	}
	missing := filepath.Join(t.TempDir(), "missing-framework.dylib")
	for _, paths := range []Output{{SharedLibPath: missing}, {CoreAudioLibPath: missing}, {CoreFoundationLibPath: missing}} {
		if _, err := paths.Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
			t.Fatal("explicit missing CoreAudio output library did not fail")
		}
		input := Input{SharedLibPath: paths.SharedLibPath, CoreAudioLibPath: paths.CoreAudioLibPath, CoreFoundationLibPath: paths.CoreFoundationLibPath}
		if _, err := input.Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
			t.Fatal("explicit missing CoreAudio input library did not fail")
		}
	}
}
func TestCoreAudioDefaultSymbolsWithoutAudioDevice(t *testing.T) {
	if _, err := loadAPI(Output{}); err != nil {
		t.Fatal(err)
	}
	if bridge.Now() <= 0 {
		t.Fatal("mach absolute clock unavailable")
	}
}

func TestOpenRejectsCancellationAndInvalidFormatBeforeLoading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := audio.Format{SampleRate: 48000, Channels: 2}
	if _, err := (Input{SharedLibPath: "must-not-load"}).Open(ctx, f); !errors.Is(err, context.Canceled) {
		t.Fatalf("input cancellation: %v", err)
	}
	if _, err := (Output{SharedLibPath: "must-not-load"}).Open(ctx, f); !errors.Is(err, context.Canceled) {
		t.Fatalf("output cancellation: %v", err)
	}
	for _, f := range []audio.Format{{SampleRate: 0, Channels: 2}, {SampleRate: 48000, Channels: 0}, {SampleRate: 48000, Channels: 9}} {
		if _, err := (Input{}).Open(context.Background(), f); err == nil {
			t.Fatalf("input accepted invalid format %+v", f)
		}
		if _, err := (Output{}).Open(context.Background(), f); err == nil {
			t.Fatalf("output accepted invalid format %+v", f)
		}
	}
}
