//go:build e2e && linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Owned by TestMain, created lazily only when the private E2E is selected.
type privatePipeWire struct {
	root, runtime string
	daemon        *exec.Cmd
	log           *os.File
	graph         *privateGraph
	done          chan struct{}
	waitErr       error
}

const privateClientConfig = `context.properties = { mem.allow-mlock = false log.level = 0 }
stream.properties = { adapter.auto-port-config = { mode = dsp monitor = false position = preserve } }
context.spa-libs = { audio.convert.* = audioconvert/libspa-audioconvert support.* = support/libspa-support }
context.modules = [
 { name = libpipewire-module-protocol-native }
 { name = libpipewire-module-client-node }
 { name = libpipewire-module-adapter }
]
`

const privateServerConfig = `context.properties = {
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

func (f *privatePipeWire) environment() map[string]string {
	return map[string]string{
		"PIPEWIRE_REMOTE": "sendspin-test", "PIPEWIRE_RUNTIME_DIR": f.runtime,
		"XDG_RUNTIME_DIR": f.runtime, "PIPEWIRE_CONFIG_DIR": f.root,
		"PIPEWIRE_CONFIG_PREFIX": "", "XDG_CONFIG_HOME": f.root, "HOME": f.root,
		"PIPEWIRE_CONFIG_NAME": "client.conf", "PIPEWIRE_CONFIG_OVERRIDES": "",
		"PIPEWIRE_PROPS": "{}",
	}
}

func startPrivatePipeWire(directory string) (_ *privatePipeWire, skip string, result error) {
	path, err := exec.LookPath("pipewire")
	if err != nil {
		return nil, "optional private software runtime unavailable: pipewire executable not installed", nil
	}
	if _, err = loadAPI(""); err != nil {
		return nil, "optional private software runtime unavailable: " + err.Error(), nil
	}
	f := &privatePipeWire{root: filepath.Join(directory, "private-pipewire")}
	defer func() {
		if result != nil {
			result = fmt.Errorf("%w; daemon log tail: %s", result, f.logTail())
			result = errors.Join(result, f.Close())
		}
	}()
	if err = os.MkdirAll(f.root, 0700); err != nil {
		return nil, "", err
	}
	// AF_UNIX paths remain short even for deeply nested source/suite paths.
	if err = os.MkdirAll("/tmp/opencode", 0700); err != nil {
		return nil, "", err
	}
	f.runtime, err = os.MkdirTemp("/tmp/opencode", "pw-")
	if err != nil {
		return nil, "", err
	}
	for name, data := range map[string]string{
		"client.conf": privateClientConfig, "client-rt.conf": privateClientConfig,
		"pipewire.conf": privateServerConfig,
	} {
		if err = os.WriteFile(filepath.Join(f.root, name), []byte(data), 0600); err != nil {
			return nil, "", err
		}
	}
	f.log, err = os.OpenFile(filepath.Join(f.root, "daemon.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, "", err
	}
	f.daemon = exec.Command(path, "-c", "pipewire.conf")
	f.daemon.Env = []string{"PATH=" + os.Getenv("PATH")}
	for key, value := range f.environment() {
		// The daemon has its explicit config, independent of client overrides.
		if key != "PIPEWIRE_CONFIG_NAME" {
			f.daemon.Env = append(f.daemon.Env, key+"="+value)
		}
	}
	f.daemon.Stdout, f.daemon.Stderr = f.log, f.log
	if err = f.daemon.Start(); err != nil {
		return nil, "", err
	}
	f.done = make(chan struct{})
	go func() { f.waitErr = f.daemon.Wait(); close(f.done) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err = os.Stat(filepath.Join(f.runtime, "sendspin-test")); err == nil {
			break
		}
		select {
		case <-f.done:
			return nil, "", fmt.Errorf("private PipeWire daemon exited before readiness: %v", f.waitErr)
		case <-ctx.Done():
			return nil, "", fmt.Errorf("private PipeWire socket not ready: %w", ctx.Err())
		case <-tick.C:
		}
	}
	// Initialization is serial and TestMain owns the fixture. Restore the caller's
	// environment immediately, so later hardware tests never inherit isolation.
	for key, value := range f.environment() {
		old, present := os.LookupEnv(key)
		defer func() {
			if present {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		}()
		if err = os.Setenv(key, value); err != nil {
			return nil, "", err
		}
	}
	f.graph, err = openPrivateGraph(ctx)
	if err != nil {
		return nil, "", err
	}
	return f, "", nil
}

func (f *privatePipeWire) logTail() string {
	data, err := os.ReadFile(filepath.Join(f.root, "daemon.log"))
	if err != nil {
		return err.Error()
	}
	return string(data[max(0, len(data)-8192):])
}

func (f *privatePipeWire) Close() error {
	var result error
	if f.graph != nil {
		result = f.graph.failure()
		f.graph.Close()
		f.graph = nil
	}
	if f.daemon != nil && f.daemon.Process != nil {
		select {
		case <-f.done:
			result = errors.Join(result, fmt.Errorf("private PipeWire daemon exited before fixture cleanup: %v", f.waitErr))
		default:
		}
		if err := f.daemon.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			result = errors.Join(result, err)
		}
		// A signal exit is expected because this is the fixture's explicit stop.
		<-f.done
		if err := f.waitErr; err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				result = errors.Join(result, err)
			}
		}
		f.daemon = nil
	}
	if f.log != nil {
		result = errors.Join(result, f.log.Close())
		f.log = nil
	}
	if f.runtime != "" {
		result = errors.Join(result, os.RemoveAll(f.runtime))
		f.runtime = ""
	}
	return result
}
