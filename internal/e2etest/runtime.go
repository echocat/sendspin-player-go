// Package e2etest owns test-only reference processes and their pinned runtimes.
// It deliberately does not import the player package.
package e2etest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/internal/devpath"
)

// Root resolves the repository independently of the test executable's cwd.
func Root() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("e2etest: cannot resolve source root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

type provision struct {
	once   sync.Once
	python string
	err    error
}

var provisions sync.Map

// Python provisions an approved locked project once per test executable. The
// exclusive lock also serializes different package test executables sharing a
// checkout, so uv never mutates a shared environment concurrently.
func Python(ctx context.Context, root, project string) (string, error) {
	if project != "e2e" && project != "conformancetest" {
		return "", fmt.Errorf("e2etest: unapproved Python project %q", project)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	value, _ := provisions.LoadOrStore(filepath.Join(root, project), &provision{})
	p := value.(*provision)
	p.once.Do(func() {
		setupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		p.python, p.err = provisionPython(setupCtx, root, project)
	})
	return p.python, p.err
}

func provisionPython(ctx context.Context, root, project string) (string, error) {
	if err := devpath.Prepare(root); err != nil {
		return "", err
	}
	unlock, err := fileLock(ctx, filepath.Join(root, "var", "python-"+project+".lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	uv, err := exec.LookPath("uv")
	if err != nil {
		uv, err = bootstrapUV(ctx, root)
		if err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, uv, "sync", "--locked", "--project", filepath.Join(root, "internal", project))
	cmd.Env = devpath.PythonEnv(root, project)
	cmd.WaitDelay = 2 * time.Second
	// Dependency failures need no potentially credential-bearing subprocess log.
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("e2etest: uv sync --locked (%s): %w", project, err)
	}
	python := devpath.Python(root, project)
	if _, err := os.Stat(python); err != nil {
		return "", fmt.Errorf("e2etest: provisioned Python missing: %w", err)
	}
	return python, nil
}

func fileLock(ctx context.Context, path string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("e2etest: waiting for runtime lock: %w", err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return sync.OnceFunc(func() { _ = os.Remove(path) }), nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("e2etest: runtime lock %s remains held (remove only after confirming its owner exited): %w", path, ctx.Err())
		case <-timer.C:
		}
	}
}

func PlaybackDevice() (string, bool)   { return device("SENDSPIN_TEST_PLAYBACK_DEVICE") }
func CaptureDevice() (string, bool)    { return device("SENDSPIN_TEST_CAPTURE_DEVICE") }
func device(key string) (string, bool) { value := os.Getenv(key); return value, value != "" }
