//go:build linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"github.com/echocat/slf4g/testing/recording"
)

// This opt-in fixture is a separate server with only a timer-driven null sink.
// No ALSA, V4L, Bluetooth, network, capture, session-manager or host server module
// is loaded. Ordinary native ABI tests do not run it or connect to any server.
func TestIsolatedPipeWirePlaybackLifecycle(t *testing.T) {
	if os.Getenv("SENDSPIN_PIPEWIRE_ISOLATED") != "1" {
		t.Skip("private software-only PipeWire fixture not requested")
	}
	artifacts := os.Getenv("SENDSPIN_PIPEWIRE_ARTIFACT_DIR")
	if !filepath.IsAbs(artifacts) {
		t.Fatal("explicit absolute artifact directory required")
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(artifacts, "run-")
	if err != nil {
		t.Fatal(err)
	}
	// Keep AF_UNIX paths short, even when the repository is under a long path.
	if err = os.MkdirAll("/tmp/opencode", 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := os.MkdirTemp("/tmp/opencode", "pw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runtime); err != nil {
			t.Error(err)
		}
	})
	for key, value := range map[string]string{"PIPEWIRE_REMOTE": "sendspin-test", "PIPEWIRE_RUNTIME_DIR": runtime, "XDG_RUNTIME_DIR": runtime, "PIPEWIRE_CONFIG_DIR": root, "PIPEWIRE_CONFIG_PREFIX": "", "XDG_CONFIG_HOME": root, "HOME": root} {
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
		if err = os.WriteFile(filepath.Join(root, name), []byte(client), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := `context.properties = {
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
   media.class = Audio/Sink node.driver = true
   audio.position = [ FL FR ]
   adapter.auto-port-config = { mode = dsp monitor = false position = preserve }
 } }
]
`
	configPath := filepath.Join(root, "pipewire.conf")
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	daemon := exec.CommandContext(ctx, "pipewire", "-c", configPath)
	daemon.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "XDG_CONFIG_HOME=" + root, "XDG_RUNTIME_DIR=" + runtime, "PIPEWIRE_RUNTIME_DIR=" + runtime, "PIPEWIRE_CONFIG_DIR=" + root}
	daemon.Stdout, daemon.Stderr = logFile, logFile
	if err = daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait() })
	for {
		if _, err = os.Stat(filepath.Join(runtime, "sendspin-test")); err == nil {
			// Socket creation precedes completion of the server's module/object
			// initialization. Probe the private protocol and sink, not just stat.
			probe := exec.CommandContext(ctx, "pw-link", "-i")
			if data, probeErr := probe.Output(); probeErr == nil && strings.Contains(string(data), "sendspin-null:") {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("private PipeWire socket did not become ready", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	reschedules := 0
	writeSilence := func(d audio.Device, samples []float32) error {
		// This is a lifecycle test on a shared CI host, not a scheduling/accuracy
		// test. If the host overtakes Next, explicitly discard and reschedule only
		// zeros. Never hide other errors or weaken the production reservation.
		for attempt := 0; attempt < 8; attempt++ {
			if _, err := d.Next(ctx); err != nil {
				return err
			}
			err := d.Write(ctx, samples)
			if !errors.Is(err, render.ErrClockReset) {
				return err
			}
			reschedules++
			var reset interface{ AudioTimelineReset() bool }
			if errors.As(err, &reset) && !reset.AudioTimelineReset() {
				continue
			}
			if err = d.Flush(ctx); err != nil {
				return err
			}
		}
		return fmt.Errorf("private fixture repeatedly missed its output reservation: %+v", d.(*render.Device).Diagnostics())
	}
	silence := make([]float32, 4800)
	for cycle := 0; cycle < 3; cycle++ {
		linkCtx, stopLink := context.WithCancel(ctx)
		linked := make(chan error, 1)
		go func() { linked <- linkPrivate(linkCtx) }()
		d, openErr := (Output{Device: "sendspin-null", Logger: recording.NewLogger()}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
		if openErr != nil {
			stopLink()
			if cleanupErr := <-linked; cleanupErr != nil {
				t.Log("link cleanup:", cleanupErr)
			}
			t.Fatal(openErr, "fixture artifacts:", root)
		}
		linkRead := false
		err = func() error {
			if _, err := d.Next(ctx); err != nil {
				return err
			}
			linkRead = true
			if err := <-linked; err != nil {
				return err
			}
			for range 10 {
				if err := writeSilence(d, silence); err != nil {
					return err
				}
			}
			if err := d.Flush(ctx); err != nil {
				return err
			}
			return writeSilence(d, silence)
		}()
		if err != nil {
			for name, args := range map[string][]string{"ports-out.log": {"-o", "-v"}, "ports-in.log": {"-i", "-v"}, "links.log": {"-l"}} {
				cmd := exec.CommandContext(ctx, "pw-link", args...)
				data, _ := cmd.CombinedOutput()
				_ = os.WriteFile(filepath.Join(root, name), data, 0600)
			}
			cmd := exec.CommandContext(ctx, "pw-dump")
			data, _ := cmd.CombinedOutput()
			_ = os.WriteFile(filepath.Join(root, "dump.json"), data, 0600)
		}
		stopLink()
		if !linkRead {
			t.Log("link result:", <-linked)
		}
		closeErr := d.Close()
		if err != nil || closeErr != nil {
			t.Fatal("private lifecycle", cycle, err, closeErr, "fixture artifacts:", root)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// A server loss must also wake a writer blocked on native backpressure.
	linkCtx, stopLink := context.WithCancel(ctx)
	defer stopLink()
	linked := make(chan error, 1)
	go func() { linked <- linkPrivate(linkCtx) }()
	d, err := (Output{Device: "sendspin-null", Logger: recording.NewLogger()}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		stopLink()
		if cleanupErr := <-linked; cleanupErr != nil {
			t.Log("link cleanup:", cleanupErr)
		}
		t.Fatal(err)
	}
	if _, err = d.Next(ctx); err != nil {
		stopLink()
		if cleanupErr := <-linked; cleanupErr != nil {
			t.Log("link cleanup:", cleanupErr)
		}
		_ = d.Close()
		t.Fatal(err)
	}
	if err = <-linked; err != nil {
		_ = d.Close()
		t.Fatal(err)
	}
	if err = writeSilence(d, silence); err != nil {
		_ = d.Close()
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() { written <- d.Write(ctx, make([]float32, 96000)) }()
	time.Sleep(20 * time.Millisecond)
	if err = daemon.Process.Kill(); err != nil {
		_ = d.Close()
		if cleanupErr := <-written; cleanupErr != nil {
			t.Log("writer cleanup:", cleanupErr)
		}
		t.Fatal(err)
	}
	_ = daemon.Wait()
	select {
	case err = <-written:
		if !errors.Is(err, render.ErrDriver) {
			_ = d.Close()
			t.Fatal("server loss did not invalidate blocked writer", err)
		}
	case <-time.After(2 * time.Second):
		_ = d.Close()
		if cleanupErr := <-written; cleanupErr != nil {
			t.Log("writer cleanup:", cleanupErr)
		}
		t.Fatal("server loss did not wake blocked writer")
	}
	_ = d.Close() // Native stop may report the already-disconnected server.
	if unexpected, openErr := (Output{Device: "sendspin-null", Logger: recording.NewLogger()}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2}); openErr == nil {
		_ = unexpected.Close()
		t.Fatal("missing private server unexpectedly opened; no fallback is allowed")
	}
	t.Log("three native lifecycle cycles and blocked-writer server-loss handling through a private software sink; no hardware measurement; explicit silence reschedules:", reschedules, root)
}

func linkPrivate(ctx context.Context) error {
	list := func(flag, prefix string) ([]string, error) {
		cmd := exec.CommandContext(ctx, "pw-link", flag)
		out, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		var ports []string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, prefix) {
				ports = append(ports, line)
			}
		}
		slices.Sort(ports)
		return ports, nil
	}
	for {
		out, _ := list("-o", "sendspin-player:")
		in, _ := list("-i", "sendspin-null:")
		if len(out) == 2 && len(in) == 2 {
			for i := range out {
				cmd := exec.CommandContext(ctx, "pw-link", out[i], in[i])
				if data, err := cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("private link: %w: %s", err, data)
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
