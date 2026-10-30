package e2etest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/devpath"
)

type Reference struct{ URL, ID string }

// Suite owns exactly one manager process, started lazily by Reference, never by
// an init hook. Each test owns only its isolated logical peers.
type Suite struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	directory string
	url       string
	cmd       *exec.Cmd
	done      chan struct{}
	waitErr   error
	startErr  error
	envErr    error
	closed    bool
	closeErr  error
}

func NewSuite() *Suite {
	ctx, cancel := context.WithCancel(context.Background())
	// Configuration is needed before m.Run even in custom TestMain variants
	// which call Close themselves. No processes/directories are created here.
	return &Suite{ctx: ctx, cancel: cancel, envErr: loadEnv(Root())}
}

func (s *Suite) Context() context.Context { return s.ctx }

func (s *Suite) directoryLocked() (string, error) {
	if s.directory != "" {
		return s.directory, nil
	}
	base := filepath.Join(Root(), "var", "e2e")
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, "suite-*")
	if err == nil {
		s.directory = dir
	}
	return dir, err
}

func (s *Suite) Directory() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.directoryLocked()
	if err != nil {
		panic(fmt.Errorf("e2etest: private suite directory: %w", err))
	}
	return dir
}

func (s *Suite) Run(m *testing.M) int {
	if err := s.envErr; err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = s.Close()
		return 1
	}
	code := m.Run()
	if err := s.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func (s *Suite) startLocked() error {
	if s.envErr != nil {
		return s.envErr
	}
	if s.closed {
		return errors.New("e2etest: suite already closed")
	}
	if s.cmd != nil {
		return s.startErr
	}
	if s.startErr != nil {
		return s.startErr
	}
	s.startErr = s.startWorkerLocked()
	return s.startErr
}

func (s *Suite) startWorkerLocked() error {
	python, err := Python(s.ctx, Root(), "e2e")
	if err != nil {
		return err
	}
	dir, err := s.directoryLocked()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(s.ctx, python, filepath.Join(Root(), "internal", "e2e", "server.py"))
	cmd.Dir = dir
	cmd.Env = devpath.PythonEnv(Root(), "e2e")
	cmd.WaitDelay = 2 * time.Second
	// Raw Python diagnostics can contain private operator input. Failures are
	// reported by phase/status only; neither settings nor raw bodies are logged.
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("e2etest: start manager: %w", err)
	}
	s.cmd = cmd
	s.done = make(chan struct{})
	go func() { s.waitErr = cmd.Wait(); close(s.done) }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	select {
	case line := <-ready:
		var message struct {
			URL  string `json:"url"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(line), &message) != nil || message.Kind != "sendspin-e2e-manager" || message.URL == "" {
			return errors.New("e2etest: manager readiness framing failed")
		}
		s.url = message.URL
		return nil
	case <-ctx.Done():
		return fmt.Errorf("e2etest: manager startup: %w", ctx.Err())
	}
}

func (s *Suite) Reference(t *testing.T, settings map[string]any) Reference {
	t.Helper()
	s.mu.Lock()
	err := s.startLocked()
	url := s.url
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	var ref Reference
	if err := request(ctx, http.MethodPost, url+"/peers", settings, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.URL == "" || ref.ID == "" {
		t.Fatal("e2etest: manager returned incomplete peer")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := request(ctx, http.MethodDelete, url+"/peers/"+ref.ID, nil, nil); err != nil {
			t.Error(err)
		}
	})
	return ref
}

func request(ctx context.Context, method, url string, payload, target any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return errors.New("e2etest: encode request")
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return errors.New("e2etest: create request")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("e2etest: manager %s request failed: %w", method, err)
	}
	// The request/status/decode result is authoritative; a read-only response
	// cleanup error must not replace it.
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("e2etest: manager %s returned %s", method, response.Status)
	}
	if target != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
			return errors.New("e2etest: decode manager response")
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return nil
}

func (s *Suite) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	s.closeErr = s.envErr
	if s.cmd != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if s.url != "" {
			s.closeErr = errors.Join(s.closeErr, request(ctx, http.MethodPost, s.url+"/shutdown", nil, nil))
		}
		cancel()
		if s.url == "" {
			s.cancel()
		}
		timer := time.NewTimer(20 * time.Second)
		select {
		case <-s.done:
			if s.waitErr != nil {
				s.closeErr = errors.Join(s.closeErr, fmt.Errorf("e2etest: manager exit: %w", s.waitErr))
			}
		case <-timer.C:
			s.cancel()
			<-s.done
			s.closeErr = errors.Join(s.closeErr, errors.New("e2etest: manager did not shut down gracefully"))
		}
		timer.Stop()
	}
	s.cancel()
	if s.directory != "" {
		s.closeErr = errors.Join(s.closeErr, os.RemoveAll(s.directory))
	}
	return s.closeErr
}
