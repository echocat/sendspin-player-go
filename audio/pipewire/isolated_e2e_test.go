//go:build e2e && linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
)

// A real dynamically loaded PipeWire stream/graph, not fake native playback.
// Only this opt-in fixture may start its own audio server, with no hardware,
// capture, session manager, Bluetooth or network modules.
func TestReferenceServerPrivatePipeWireScenario(t *testing.T) {
	if os.Getenv("SENDSPIN_PIPEWIRE_ISOLATED_E2E") != "1" {
		t.Skip("private PipeWire independent-server fixture not requested; no host server connected")
	}
	root := os.Getenv("SENDSPIN_PIPEWIRE_E2E_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("explicit absolute repository root required")
	}
	private := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	// Build before isolating HOME, preserving the caller's normal Go caches.
	tool := filepath.Join(private, "loopback-tool")
	build := exec.CommandContext(ctx, "go", "build", "-o", tool, ".")
	build.Dir = filepath.Join(root, "_examples/loopback")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if err := build.Run(); err != nil {
		t.Fatal("build offline reference tool:", err)
	}
	reference := filepath.Join(private, "reference.wav")
	if err := exec.CommandContext(ctx, tool, "generate", "-out", reference, "-seconds", "3", "-amplitude", "0.01").Run(); err != nil {
		t.Fatal(err)
	}
	// Keep AF_UNIX socket paths short even in deeply nested checkouts.
	if err := os.MkdirAll("/tmp/opencode", 0700); err != nil {
		t.Fatal(err)
	}
	runtimeDir, err := os.MkdirTemp("/tmp/opencode", "pw-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runtimeDir); err != nil {
			t.Error(err)
		}
	})
	for key, value := range map[string]string{"PIPEWIRE_REMOTE": "sendspin-test", "PIPEWIRE_RUNTIME_DIR": runtimeDir, "XDG_RUNTIME_DIR": runtimeDir, "PIPEWIRE_CONFIG_DIR": private, "PIPEWIRE_CONFIG_PREFIX": "", "XDG_CONFIG_HOME": private, "HOME": private} {
		t.Setenv(key, value)
	}
	client := `context.properties = { mem.allow-mlock = false log.level = 0 }
stream.properties = { adapter.auto-port-config = { mode = dsp monitor = false position = preserve } }
context.spa-libs = { audio.convert.* = audioconvert/libspa-audioconvert support.* = support/libspa-support }
context.modules = [
 { name = libpipewire-module-protocol-native }
 { name = libpipewire-module-client-node }
 { name = libpipewire-module-adapter }
]
`
	for _, name := range []string{"client.conf", "client-rt.conf"} {
		if err := os.WriteFile(filepath.Join(private, name), []byte(client), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := `context.properties = {
 core.daemon = true core.name = sendspin-test
 default.clock.rate = 48000 default.clock.quantum = 1024
 mem.allow-mlock = false support.dbus = false log.level = 1
}
context.spa-libs = { audio.convert.* = audioconvert/libspa-audioconvert support.* = support/libspa-support }
context.modules = [
 { name = libpipewire-module-protocol-native }
 { name = libpipewire-module-client-node }
 { name = libpipewire-module-adapter }
 { name = libpipewire-module-link-factory }
 { name = libpipewire-module-access args = { access.force = unrestricted } }
]
context.objects = [
 { factory = adapter args = {
   factory.name = support.null-audio-sink node.name = sendspin-null
   media.class = Audio/Sink node.driver = true audio.position = [ FL FR ]
   adapter.auto-port-config = { mode = dsp monitor = false position = preserve }
 } }
]
`
	config := filepath.Join(private, "pipewire.conf")
	if err := os.WriteFile(config, []byte(server), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(private, "daemon.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	daemon := exec.CommandContext(ctx, "pipewire", "-c", config)
	daemon.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + private, "XDG_CONFIG_HOME=" + private, "XDG_RUNTIME_DIR=" + runtimeDir, "PIPEWIRE_RUNTIME_DIR=" + runtimeDir, "PIPEWIRE_CONFIG_DIR=" + private}
	daemon.Stdout, daemon.Stderr = log, log
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait() })
	readyCtx, stopReady := context.WithTimeout(ctx, 10*time.Second)
	defer stopReady()
	for {
		probe := exec.CommandContext(readyCtx, "pw-link", "-i")
		if b, err := probe.Output(); err == nil && strings.Contains(string(b), "sendspin-null:") {
			break
		}
		select {
		case <-readyCtx.Done():
			t.Fatal("private graph did not become ready", readyCtx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	for key, value := range map[string]string{"SENDSPIN_PIPEWIRE_E2E_PLAYBACK": "1", "SENDSPIN_PIPEWIRE_E2E_VIRTUAL": "", "SENDSPIN_PIPEWIRE_E2E_DEVICE": "sendspin-null", "SENDSPIN_PIPEWIRE_E2E_ARTIFACT_DIR": private, "SENDSPIN_PIPEWIRE_REFERENCE_FILE": reference} {
		t.Setenv(key, value)
	}
	audiotest.RunReferenceServerPlaybackScenario(t, audiotest.ScenarioConfig{Prefix: "SENDSPIN_PIPEWIRE", SoftwareFixture: true, OutputFactory: func(device string) audio.Output { return privateLinkedOutput{Output{Device: device}} }})
	t.Log("Independent encrypted server -> SDK PCM/FLAC -> real private PipeWire null graph; software integration only, no physical output/capture claim")
}

type privateLinkedOutput struct{ Output }
type privateLinkedDevice struct {
	audio.Device
	cancel context.CancelFunc
	linked <-chan error
	once   sync.Once
	err    error
}

func (o privateLinkedOutput) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	linkCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- linkPrivate(linkCtx) }()
	d, err := o.Output.Open(ctx, f)
	if err != nil {
		cancel()
		linkErr := <-done
		if errors.Is(linkErr, context.Canceled) {
			linkErr = nil
		}
		return nil, errors.Join(err, linkErr)
	}
	return &privateLinkedDevice{Device: d, cancel: cancel, linked: done}, nil
}
func (d *privateLinkedDevice) Close() error {
	d.once.Do(func() {
		d.cancel()
		linkErr := <-d.linked
		if errors.Is(linkErr, context.Canceled) {
			linkErr = nil
		}
		d.err = errors.Join(d.Device.Close(), linkErr)
	})
	return d.err
}
