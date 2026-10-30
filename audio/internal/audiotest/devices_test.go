package audiotest

import "testing"

func TestMissingDeviceOptInDoesNotConstructBackend(t *testing.T) {
	t.Setenv("SENDSPIN_TEST_AUDIO_BACKEND", "")
	t.Setenv(PlaybackDeviceEnv, "")
	t.Setenv(CaptureDeviceEnv, "")
	for _, name := range []string{"loopback", "playback", "source"} {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "loopback":
				LoopbackDevices(t, "SENDSPIN_ALSA")
			case "playback":
				PlaybackDevice(t, "SENDSPIN_ALSA")
			case "source":
				// Playback consent never authorizes capture.
				t.Setenv(PlaybackDeviceEnv, "not-source-consent")
				CaptureDevice(t, "SENDSPIN_ALSA")
			}
			t.Fatal("missing device consent reached backend construction")
		})
	}
	t.Run("loopback-needs-separate-capture", func(t *testing.T) {
		t.Setenv(PlaybackDeviceEnv, "not-capture-consent")
		LoopbackDevices(t, "SENDSPIN_ALSA")
		t.Fatal("playback consent reached loopback capture")
	})
}

func TestAudioBackendConfigurationRejectsUnknownAndSoftware(t *testing.T) {
	for _, value := range []string{"", "alsa", "pipewire", "wasapi", "coreaudio"} {
		if got, err := parseAudioBackend(value); err != nil || got != value {
			t.Fatal("native backend setting changed or rejected", got, err)
		}
	}
	for _, value := range []string{"unknown", "noop", "auto", "ALSA", " "} {
		if _, err := parseAudioBackend(value); err == nil {
			t.Fatal("invalid/backend software fallback accepted", value)
		}
	}
	t.Setenv("SENDSPIN_TEST_AUDIO_BACKEND", "alsa")
	t.Setenv(PlaybackDeviceEnv, "explicit-device")
	completed := false
	t.Run("legacy-prefix-does-not-filter", func(t *testing.T) {
		if device := PlaybackDevice(t, "SENDSPIN_PIPEWIRE"); device != "explicit-device" {
			t.Fatal("legacy prefix affected device/backend configuration")
		}
		completed = true
	})
	if !completed {
		t.Fatal("mismatched legacy Prefix hid configuration behind a skip")
	}
}
