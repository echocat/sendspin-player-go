//go:build e2e

package e2etest

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"testing"
	"time"
)

var testSuite = NewSuite()

func TestMain(m *testing.M) { os.Exit(testSuite.Run(m)) }

func TestE2ESuiteOneWorkerManyIsolatedPeers(t *testing.T) {
	settings := map[string]any{"client_id": "fixture-client", "psk": base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	var pid float64
	status := func() map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(testSuite.Context(), 5*time.Second)
		defer cancel()
		var result map[string]any
		if err := request(ctx, http.MethodGet, testSuite.url+"/status", nil, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("four simultaneous peers", func(t *testing.T) {
		ids, urls := map[string]bool{}, map[string]bool{}
		for range 4 {
			ref := testSuite.Reference(t, settings)
			if ids[ref.ID] || urls[ref.URL] {
				t.Fatal("peer identity/listener reused")
			}
			ids[ref.ID], urls[ref.URL] = true, true
		}
		snapshot := status()
		pid = snapshot["pid"].(float64)
		if snapshot["peers"] != float64(4) {
			t.Fatal("manager did not retain four peers")
		}
	})
	if got := status(); got["peers"] != float64(0) || got["peer_tasks"] != float64(0) || got["pid"] != pid {
		t.Fatal("peer cleanup stopped or leaked the worker")
	}
	t.Run("failed startup leaves no peer or tasks", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(testSuite.Context(), 5*time.Second)
		defer cancel()
		if err := request(ctx, http.MethodPost, testSuite.url+"/peers", map[string]any{"client_id": "incomplete-fixture"}, nil); err == nil {
			t.Fatal("incomplete settings accepted")
		}
		if got := status(); got["peers"] != float64(0) || got["peer_tasks"] != float64(0) || got["pid"] != pid {
			t.Fatal("failed startup leaked or restarted the worker")
		}
	})
	t.Run("peer lifetime does not limit worker lifetime", func(t *testing.T) {
		short := map[string]any{"client_id": settings["client_id"], "psk": settings["psk"], "lifetime_seconds": 0.02}
		_ = testSuite.Reference(t, short)
		deadline := time.Now().Add(5 * time.Second)
		for {
			got := status()
			if got["pid"] != pid {
				t.Fatal("expired peer restarted the worker")
			}
			if got["peer_tasks"] == float64(0) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("expired peer leaked tasks")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	t.Run("next peer reuses worker", func(t *testing.T) {
		_ = testSuite.Reference(t, settings)
		if got := status(); got["pid"] != pid || got["peers"] != float64(1) {
			t.Fatal("worker restarted")
		}
	})
}
