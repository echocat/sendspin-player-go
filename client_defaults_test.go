package sendspin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRecommendedListenerDefaultsAndActualServePath(t *testing.T) {
	_, port, err := net.SplitHostPort(RecommendedListenAddress)
	if err != nil || port != "8928" || DefaultWebSocketPath != "/sendspin" {
		t.Fatal("recommended defaults changed", err)
	}
	c, err := New(context.Background(), Config{Name: "Explicit listener defaults", Store: &MemoryStore{}, Roles: []Role{}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Serve(ctx, l) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve did not stop")
		}
	}()
	httpClient := &http.Client{Timeout: 2 * time.Second}
	for _, path := range []string{DefaultWebSocketPath, "/not-sendspin"} {
		res, err := httpClient.Get("http://" + l.Addr().String() + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if path == DefaultWebSocketPath {
			if res.StatusCode == http.StatusNotFound || res.StatusCode < 400 {
				t.Fatal("default route was not the WebSocket handler", res.StatusCode)
			}
		} else if res.StatusCode != http.StatusNotFound {
			t.Fatal("unconfigured route was mounted", res.StatusCode)
		}
	}
}
