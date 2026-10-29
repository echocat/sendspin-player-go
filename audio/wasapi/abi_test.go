//go:build windows && (amd64 || arm64)

package wasapi

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
	"golang.org/x/sys/windows"
)

func TestWASAPIFormatAndStrictLibrary(t *testing.T) {
	b, err := waveFormat(48000, 2)
	if err != nil || binary.LittleEndian.Uint16(b[16:]) != 22 || binary.LittleEndian.Uint16(b[18:]) != 32 || binary.LittleEndian.Uint32(b[20:]) != 3 || binary.LittleEndian.Uint32(b[24:]) != 3 {
		t.Fatal("WAVEFORMATEXTENSIBLE packing changed", err)
	}
	if _, err = waveFormat(48000, 3); err == nil {
		t.Fatal("unknown speaker layout accepted")
	}
	if _, err = (Output{SharedLibPath: filepath.Join(t.TempDir(), "absent-ole32.dll")}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		t.Fatal("missing explicit WASAPI library did not fail")
	}
	if _, err = (Input{SharedLibPath: filepath.Join(t.TempDir(), "absent-ole32.dll")}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		t.Fatal("missing explicit WASAPI input library did not fail")
	}
	for channels, mask := range map[int]uint32{1: 4, 2: 3, 4: 0x33, 6: 0x3f, 8: 0x63f} {
		b, err := waveFormat(96000, channels)
		if err != nil || len(b) != 40 || binary.LittleEndian.Uint16(b[0:]) != 0xfffe || binary.LittleEndian.Uint16(b[2:]) != uint16(channels) || binary.LittleEndian.Uint32(b[4:]) != 96000 || binary.LittleEndian.Uint32(b[8:]) != uint32(96000*channels*4) || binary.LittleEndian.Uint16(b[12:]) != uint16(channels*4) || binary.LittleEndian.Uint16(b[14:]) != 32 || binary.LittleEndian.Uint32(b[20:]) != mask {
			t.Fatalf("invalid packed format for %d channels: %x, %v", channels, b, err)
		}
		if got := b[24:]; string(got) != string([]byte{3, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71}) {
			t.Fatalf("invalid IEEE float subformat: %x", got)
		}
	}
	for _, rate := range []int{-1, 0, 7999, 384001} {
		if _, err := waveFormat(rate, 2); err == nil {
			t.Fatalf("invalid rate %d accepted", rate)
		}
	}
}
func TestWASAPIDefaultSymbolsWithoutAudioDevice(t *testing.T) {
	if _, err := loadAPI(""); err != nil {
		t.Fatal(err)
	}
}

func TestWASAPIOle32FunctionABIWithoutAudioDevice(t *testing.T) {
	a, err := loadAPI("")
	if err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := waError(a.initialize(0, 0)); err != nil {
		t.Fatal(err)
	}
	defer a.uninitialize()
	hr := a.initialize(0, 0)
	if hr < 0 {
		t.Fatal(waError(hr))
	}
	defer a.uninitialize()
	if hr != 1 {
		t.Fatal("second CoInitializeEx must return S_FALSE", hr)
	}
	// CLSID_NULL cannot create an object. Exercise the bound argument/output and
	// signed HRESULT ABI without ever activating an audio endpoint or service.
	var object unsafe.Pointer
	var zero windows.GUID
	if hr := a.create(&zero, 0, 23, &zero, &object); hr >= 0 || object != nil {
		release(object)
		t.Fatalf("CoCreateInstance invalid-class ABI: HRESULT=%d object=%p", hr, object)
	}
}

func TestWASAPIExplicitLibraryMissingSymbolDoesNotFallback(t *testing.T) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	// kernel32 is deliberately a real DLL without COM exports. A successful
	// default ole32 binding must not hide the missing explicitly chosen symbol.
	if _, err = loadAPI(filepath.Join(dir, "kernel32.dll")); err == nil || !strings.Contains(err.Error(), "CoInitializeEx") {
		t.Fatalf("missing explicit symbol did not fail strictly: %v", err)
	}
}
