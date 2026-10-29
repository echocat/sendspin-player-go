//go:build e2e

package audiotest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/internal/devpath"
)

type scenarioReference struct{ url, id string }

func (r scenarioReference) control(ctx context.Context, command string) (map[string]any, error) {
	b, _ := json.Marshal(map[string]string{"command": command})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/control", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("scenario command %s: HTTP %d", command, resp.StatusCode)
	}
	var out map[string]any
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
	return out, err
}
func startScenarioReference(t *testing.T, ctx context.Context, root, dir, reference string, cred sendspin.Credentials, key sendspin.Secret) scenarioReference {
	t.Helper()
	return startScenarioReferenceWithOptions(t, ctx, root, dir, cred, key, map[string]any{"weak_reference_path": reference})
}

func startScenarioReferenceWithOptions(t *testing.T, ctx context.Context, root, dir string, cred sendspin.Credentials, key sendspin.Secret, options map[string]any) scenarioReference {
	t.Helper()
	if err := devpath.Prepare(root); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"client_id": cred.ClientID(), "psk": base64.RawURLEncoding.EncodeToString(key[:])}
	for name, value := range options {
		settings[name] = value
	}
	b, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "server-private.json")
	f, err := os.OpenFile(config, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal("cannot save private scenario configuration")
	}
	serverCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(serverCtx, devpath.Python(root, "e2e"), "internal/e2e/server.py", "--config", config)
	cmd.Dir = root
	cmd.Env = devpath.PythonEnv(root, "e2e")
	cmd.WaitDelay = 2 * time.Second
	log, err := os.OpenFile(filepath.Join(dir, "server.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = log
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatal("independent server could not start; run the mise E2E task to prepare its pinned environment")
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		err := <-finished
		if t.Failed() {
			t.Log("independent server shutdown:", err)
		}
		_ = log.Close()
	})
	ready := make(chan []byte, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if scanner.Scan() {
			ready <- append([]byte(nil), scanner.Bytes()...)
		} else {
			ready <- nil
		}
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case line := <-ready:
		var response struct {
			URL string `json:"url"`
			ID  string `json:"server_id"`
		}
		if json.Unmarshal(line, &response) != nil || response.URL == "" || response.ID == "" {
			t.Fatal("independent server did not report readiness; see private server.log")
		}
		return scenarioReference{response.URL, response.ID}
	case <-timer.C:
		t.Fatal("independent server startup timeout")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return scenarioReference{}
}
