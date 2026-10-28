package sendspin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDuplicateIdleLastPlaybackIdentityCannotReplaceItsHolder(t *testing.T) {
	cfg := schemaConfig()
	cfg.Roles = []Role{Controller}
	s, p := newSchemaSession(t, cfg)
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, false))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": cfg.Roles})
	expectSchemaPayload(t, p, "client/state", map[string]any{"available": true})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(finished); s.client.ServeHTTP(w, r) }))
	defer server.Close()
	defer close(s.done)
	conn, _, err := websocket.Dial(s.ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	duplicate := &handshakePeer{ctx: s.ctx, conn: conn, suite: p.suite, clientKey: bytes.Clone(p.clientKey), serverKey: bytes.Clone(p.serverKey), serverID: p.serverID, pskID: p.pskID, psk: bytes.Clone(p.psk)}
	if err = duplicate.initialTransport(); err != nil {
		t.Fatal(err)
	}
	if err = duplicate.sendSecure(peerJSON("server/hello", map[string]any{"name": "Same server, second socket"})); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := duplicate.readSecure(); err != nil || kind != "client/hello" {
		t.Fatal(err)
	}
	if err = duplicate.sendSecure(peerJSON("server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{}})); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, duplicate, "client/goodbye", map[string]any{"reason": "concurrent_attempt"})
	if err = duplicate.closedWithoutData(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("duplicate handler did not close/release")
	}
	s.client.mu.Lock()
	preserved := s.client.active == s && !s.retiring && len(s.client.sessions) == 1 && s.client.modeUsers == 0
	s.client.mu.Unlock()
	if !preserved {
		t.Fatal("duplicate last-playback identity displaced/retired the holder")
	}
	schemaReceive(t, s, p, "group/update", map[string]any{"group_id": "holder-still-live", "group_name": "holder", "playback_state": "stopped"})
	if s.client.Snapshot().Group.ID != "holder-still-live" {
		t.Fatal("holder no longer usable")
	}
}
