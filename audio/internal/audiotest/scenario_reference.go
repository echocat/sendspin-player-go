//go:build e2e

package audiotest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/internal/e2etest"
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
func startScenarioReference(t *testing.T, suite *e2etest.Suite, reference string, cred sendspin.Credentials, key sendspin.Secret) scenarioReference {
	t.Helper()
	return startScenarioReferenceWithOptions(t, suite, cred, key, map[string]any{"weak_reference_path": reference})
}

func startScenarioReferenceWithOptions(t *testing.T, suite *e2etest.Suite, cred sendspin.Credentials, key sendspin.Secret, options map[string]any) scenarioReference {
	t.Helper()
	if suite == nil {
		t.Fatal("reference scenario requires an explicit package Suite")
	}
	settings := map[string]any{"client_id": cred.ClientID(), "psk": base64.RawURLEncoding.EncodeToString(key[:])}
	for name, value := range options {
		settings[name] = value
	}
	ref := suite.Reference(t, settings)
	return scenarioReference{ref.URL, ref.ID}
}
