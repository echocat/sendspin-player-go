//go:build unix

package examples

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func ignoreFixtureInterrupts() { signal.Ignore(os.Interrupt, syscall.SIGTERM) }

func TestCancellationInterruptsDescendants(t *testing.T) {
	t.Setenv("SENDSPIN_EXAMPLE_PROCESS_FIXTURE", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	var output bytes.Buffer
	dir := t.TempDir()
	go func() {
		done <- Run(ctx, dir, Command{Tool: binary, Args: []string{"-test.run=^TestProcessFixture$", "--", "group"}}, nil, io.MultiWriter(&output, readyWriter{ready}), nil)
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("fixture stopped before readiness: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("descendant never became ready")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
		for _, text := range []string{"child interrupted", "parent reaped child"} {
			if !strings.Contains(output.String(), text) {
				t.Fatalf("missing %q in %q", text, output.String())
			}
		}
	case <-time.After(3 * shutdownGrace):
		t.Fatal("descendant did not stop within the shutdown bound")
	}
}
