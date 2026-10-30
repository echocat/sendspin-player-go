//go:build e2e

package native_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
	"github.com/echocat/sendspin-player-go/audio/native"
)

func configuredBackend(t *testing.T) native.Backend {
	t.Helper()
	backend := native.Backend(audiotest.AudioBackend(t))
	if (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") ||
		(runtime.GOOS != "linux" && runtime.GOOS != "windows" && runtime.GOOS != "darwin") {
		t.Skip("native audio E2E is unsupported on this OS/architecture; no devices or peers opened")
	}
	if backend != native.BackendAuto {
		supported := runtime.GOOS == "linux" && (backend == native.BackendALSA || backend == native.BackendPipeWire) ||
			runtime.GOOS == "windows" && backend == native.BackendWASAPI ||
			runtime.GOOS == "darwin" && backend == native.BackendCoreAudio
		if !supported {
			t.Fatalf("explicit audio backend %q is unavailable on %s; no fallback", backend, runtime.GOOS)
		}
	}
	return backend
}

func TestE2EReferenceServerPlaybackScenario(t *testing.T) {
	backend := configuredBackend(t)
	audiotest.RunReferenceServerPlaybackScenario(t, audiotest.ScenarioConfig{
		Suite: suite,
		OutputFactory: func(device string) audio.Output {
			return native.Output{Backend: backend, Device: device}
		},
	})
}

func TestE2EReferenceServerNativeInputScenario(t *testing.T) {
	backend := configuredBackend(t)
	audiotest.RunReferenceServerNativeInputScenario(t, audiotest.SourceScenarioConfig{
		Suite: suite,
		InputFactory: func(device string) audio.Input {
			// Source always opens a normal, separately authorized capture input.
			return native.Input{Backend: backend, Device: device}
		},
	})
}

// Observe the SDK's real selection, then pin capture to that backend. Auto is
// never independently retried against another backend's capture-ID namespace.
type selectedOutput struct {
	native.Output
	selected *native.Backend
}

func (o selectedOutput) Open(ctx context.Context, format audio.Format) (audio.Device, error) {
	d, err := o.Output.Open(ctx, format)
	if err != nil {
		return d, err
	}
	selected, ok := d.(native.SelectedBackend)
	if !ok {
		return nil, errors.Join(errors.New("native output did not identify its selected backend"), d.Close())
	}
	*o.selected = selected.Backend()
	return d, nil
}

func duplexConfig(backend native.Backend, loopback bool) audiotest.LoopbackConfig {
	selected := backend
	return audiotest.LoopbackConfig{
		Suite: suite,
		OutputFactory: func(device string) audio.Output {
			return selectedOutput{Output: native.Output{Backend: backend, Device: device}, selected: &selected}
		},
		InputFactory: func(device string) audio.Input {
			// WASAPI/PipeWire expose render-monitor capture. ALSA/CoreAudio use
			// the separately authorized ordinary input, as their former wrappers did.
			monitor := loopback && (selected == native.BackendWASAPI || selected == native.BackendPipeWire)
			return native.Input{Backend: selected, Device: device, Loopback: monitor}
		},
	}
}

func TestE2ENativeLoopback(t *testing.T) {
	audiotest.RunNativeLoopback(t, duplexConfig(configuredBackend(t), true))
}

func TestE2EHardwareFullDuplexSilenceCapture(t *testing.T) {
	audiotest.RunFullDuplexSilenceCapture(t, duplexConfig(configuredBackend(t), false))
}

func TestE2EHardwareWeakReferencePlayback(t *testing.T) {
	backend := configuredBackend(t)
	audiotest.RunWeakReferencePlayback(t, audiotest.ScenarioConfig{
		Suite: suite,
		OutputFactory: func(device string) audio.Output {
			return native.Output{Backend: backend, Device: device}
		},
	})
}

// Three reopen cycles, digital-zero writes, Flush, canceled Write and repeated
// Close preserve the former ALSA lifecycle checks through native interfaces.
func TestE2EHardwareSilenceLifecycle(t *testing.T) {
	backend := configuredBackend(t)
	device := audiotest.PlaybackDevice(t, "")
	ctx, cancel := context.WithTimeout(suite.Context(), 15*time.Second)
	defer cancel()
	for range 3 {
		d, err := (native.Output{Backend: backend, Device: device}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
		if err != nil {
			t.Fatal("explicit native output failed; no fallback:", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		err = func() error {
			t.Log("selected native backend:", d.(native.SelectedBackend).Backend())
			if _, err := d.Next(ctx); err != nil {
				return err
			}
			for range 20 {
				if err := d.Write(ctx, make([]float32, 960)); err != nil {
					return err
				}
			}
			if err := d.Flush(ctx); err != nil {
				return err
			}
			if _, err := d.Next(ctx); err != nil {
				return err
			}
			canceled, stop := context.WithCancel(ctx)
			stop()
			if err := d.Write(canceled, make([]float32, 960)); !errors.Is(err, context.Canceled) {
				return errors.New("canceled write did not preserve cancellation")
			}
			return nil
		}()
		if err = errors.Join(err, d.Close()); err != nil {
			t.Fatal(err)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
