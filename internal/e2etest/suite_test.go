package e2etest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSuiteConstructionIsLazyAndCloseIsIdempotent(t *testing.T) {
	s := NewSuite()
	if s.cmd != nil || s.directory != "" || s.Context().Err() != nil {
		t.Fatal("construction performed setup")
	}
	for range 2 {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(s.Context().Err(), context.Canceled) {
		t.Fatal("suite context not canceled")
	}
	s.mu.Lock()
	err := s.startLocked()
	s.mu.Unlock()
	if err == nil {
		t.Fatal("closed suite accepted restart")
	}
}

func TestRuntimeLockCancellationAndSerialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lock")
	unlock, err := fileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := fileLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contender did not honor cancellation: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("contender removed owner's lock")
	}
	unlock()
	unlocked, err := fileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	unlocked()
}

func TestManagerRequestDoesNotExposeSettingsOrResponseSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, "secret-response-marker")
	}))
	defer server.Close()
	err := request(context.Background(), http.MethodPost, server.URL, map[string]any{"psk": "secret-settings-marker"}, nil)
	if err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("unsafe failure: %v", err)
	}
}

func TestManagerRequestCancellationJoinsHandler(t *testing.T) {
	stopped := make(chan struct{})
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- request(ctx, http.MethodGet, server.URL, nil, nil) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request ignored cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled request leaked")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled handler leaked")
	}
}

// This is a Go fake process, not a second reference implementation or Python
// worker. It lets unit tests prove Wait is joined before Close returns.
func TestSuiteFakeWorker(t *testing.T) {
	if os.Getenv("SENDSPIN_SUITE_FAKE_WORKER") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestSuiteCloseStopsAndJoinsWorkerExactlyOnce(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSuiteFakeWorker$")
	cmd.Env = append(os.Environ(), "SENDSPIN_SUITE_FAKE_WORKER=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	var stops atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		stops.Add(1)
		_ = input.Close()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	s := NewSuite()
	s.cmd, s.url, s.done = cmd, server.URL, make(chan struct{})
	s.directory, err = os.MkdirTemp(t.TempDir(), "private-")
	if err != nil {
		t.Fatal(err)
	}
	go func() { s.waitErr = cmd.Wait(); close(s.done) }()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 {
		t.Fatal("shutdown was repeated")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("Close did not join worker")
	}
	if _, err := os.Stat(s.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private directory retained")
	}
}

func TestPythonRejectsUnapprovedProjectsBeforeSetup(t *testing.T) {
	if _, err := Python(context.Background(), t.TempDir(), "arbitrary"); err == nil {
		t.Fatal("unapproved project accepted")
	}
}
