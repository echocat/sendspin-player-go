package sendspin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestServeHTTPRejectsDirectTLSBeforeUpgrade(t *testing.T) {
	client, err := New(context.Background(), Config{Name: "Plain Sendspin", Store: &MemoryStore{}, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(client)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client()})
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("TLS was upgraded instead of rejected: conn=%v status=%v err=%v", conn, response, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.modeUsers != 0 || len(client.sessions) != 0 {
		t.Fatal("rejected transport reserved a mode/session")
	}
}

func TestConnectDoesNotFollowWebSocketRedirects(t *testing.T) {
	for _, scheme := range []string{"http", "https", "ws", "wss"} {
		t.Run(scheme, func(t *testing.T) {
			var connections atomic.Int32
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err == nil {
					_ = conn.CloseNow()
				}
			}))
			target.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			if scheme == "https" || scheme == "wss" {
				target.StartTLS()
			} else {
				target.Start()
			}
			defer target.Close()
			destination := scheme + target.URL[strings.Index(target.URL, ":"):]
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination, http.StatusTemporaryRedirect)
			}))
			defer redirect.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := New(ctx, Config{Name: "No redirects", Store: &MemoryStore{}, Roles: []Role{Controller}})
			if err != nil {
				t.Fatal(err)
			}
			if err = client.Connect(ctx, "ws"+strings.TrimPrefix(redirect.URL, "http")); err == nil {
				t.Fatal("redirect unexpectedly established a connection")
			}
			if connections.Load() != 0 {
				t.Fatal("contacted redirect target instead of rejecting its response")
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			if client.modeUsers != 0 || len(client.sessions) != 0 {
				t.Fatal("redirect rejection leaked a mode/session reservation")
			}
		})
	}
}
