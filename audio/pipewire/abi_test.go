//go:build linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestPipeWireABIAndNativePODVector(t *testing.T) {
	if unsafe.Sizeof(spaData{}) != 40 || unsafe.Offsetof(spaData{}.Data) != 24 || unsafe.Sizeof(spaBuffer{}) != 24 || unsafe.Sizeof(pwTime{}) != 64 || unsafe.Sizeof(pwEvents{}) != 96 || unsafe.Offsetof(clockPrefix{}.Duration) != 96 {
		t.Fatal("PipeWire/SPAv0 ABI layout changed")
	}
	const vector = "800000000f0000000300040003000000010000000000000004000000030000000100000000000000020000000000000004000000030000000100000000000000010001000000000004000000030000001b010000000000000300010000000000040000000400000080bb000000000000040001000000000004000000040000000200000000000000"
	if hex.EncodeToString(rawFormat(48000, 2)) != vector {
		t.Fatal("SPA POD differs from independent C header builder")
	}
}
func TestExplicitPipeWireLibraryFailureDoesNotConnect(t *testing.T) {
	o := Output{SharedLibPath: filepath.Join(t.TempDir(), "absent-pipewire.so")}
	if _, err := o.Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		t.Fatal("missing explicit library did not fail")
	}
}
