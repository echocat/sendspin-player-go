// Package audiotest contains test fixtures only, never production audio paths.
package audiotest

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const (
	PlaybackDeviceEnv = "SENDSPIN_TEST_PLAYBACK_DEVICE"
	CaptureDeviceEnv  = "SENDSPIN_TEST_CAPTURE_DEVICE"
)

// AudioBackend validates configuration without selecting another backend or
// hiding mistakes behind a skip. Empty delegates selection to the native SDK.
func AudioBackend(t *testing.T) string {
	t.Helper()
	selected, err := parseAudioBackend(os.Getenv("SENDSPIN_TEST_AUDIO_BACKEND"))
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func parseAudioBackend(selected string) (string, error) {
	switch selected {
	case "", "alsa", "pipewire", "wasapi", "coreaudio":
		return selected, nil
	default:
		return "", errors.New("SENDSPIN_TEST_AUDIO_BACKEND must be empty, alsa, pipewire, wasapi or coreaudio; no software fallback")
	}
}

func requireDevices(t *testing.T, _ string, names ...string) []string {
	t.Helper()
	AudioBackend(t)
	values := make([]string, len(names))
	for i, name := range names {
		values[i] = os.Getenv(name)
		if values[i] == "" {
			t.Skip(name + " not set; no native library, device or peer opened")
		}
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
			t.Fatal("invalid explicitly supplied audio device; no default fallback")
		}
	}
	return values
}

func PlaybackDevice(t *testing.T, prefix string) string {
	t.Helper()
	return requireDevices(t, prefix, PlaybackDeviceEnv)[0]
}

func CaptureDevice(t *testing.T, prefix string) string {
	t.Helper()
	return requireDevices(t, prefix, CaptureDeviceEnv)[0]
}

func LoopbackDevices(t *testing.T, prefix string) (playback, capture string) {
	t.Helper()
	devices := requireDevices(t, prefix, PlaybackDeviceEnv, CaptureDeviceEnv)
	return devices[0], devices[1]
}
