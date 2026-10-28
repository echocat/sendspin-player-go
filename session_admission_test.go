package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func TestPairingCompletionReleasesAttemptProtection(t *testing.T) {
	ctx := context.Background()
	store := &MemoryStore{}
	client, err := New(ctx, Config{Name: "Pairing state", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, ctx, func() {})
	s.channel = &wire.Channel{ServerID: server.ClientID(), Category: "pr"}
	s.rank = 1
	psk := Secret{12}
	s.pendingPSK = &psk
	s.setPairingAttempt(true)
	if err = s.finalizePairing([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if s.pairingAttempt || s.pendingPSK != nil {
		t.Fatal("completed attempt still blocks admission")
	}
	if s.rank != 1 {
		t.Fatal("completion changed declared activity priority")
	}
}

func TestDecidedDisplacementPreventsALatePairInit(t *testing.T) {
	client := &Client{}
	old := newSession(client, context.Background(), func() {})
	old.channel = &wire.Channel{ServerID: "old"}
	client.active = old
	incoming := newSession(client, context.Background(), func() {})
	incoming.channel = &wire.Channel{ServerID: "incoming"}
	done := make(chan error, 1)
	go func() { done <- client.admit(incoming, 2, false) }()
	select {
	case <-old.displace:
	case <-time.After(time.Second):
		close(old.done)
		t.Fatal("admission did not decide displacement")
	}
	if old.beginPairingAttempt() {
		close(old.done)
		t.Fatal("pairing started after handoff was decided")
	}
	close(old.done)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.active != incoming {
		t.Fatal("handoff did not complete")
	}
}

func TestAdmissionPublishesDeclaredRankWithTheHolder(t *testing.T) {
	for _, rank := range []int{0, 1, 2} {
		client := &Client{}
		s := newSession(client, context.Background(), func() {})
		s.channel = &wire.Channel{ServerID: "incoming"}
		if err := client.admit(s, rank, rank == 1); err != nil {
			t.Fatal(err)
		}
		// Another admission may acquire the lock immediately on return, before
		// the first actor persists playback state or publishes its role set.
		client.mu.Lock()
		published, got := client.active, s.rank
		client.mu.Unlock()
		if published != s || got != rank {
			t.Fatal("holder was exposed with stale priority", rank, got)
		}
	}
}

func TestCanceledAdmissionCannotRetireTheCurrentHolder(t *testing.T) {
	client := &Client{}
	old := newSession(client, context.Background(), func() {})
	old.channel = &wire.Channel{ServerID: "holder"}
	old.rank = 2
	client.active = old
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	incoming := newSession(client, ctx, func() {})
	incoming.channel = &wire.Channel{ServerID: "canceled"}
	if err := client.admit(incoming, 2, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if client.active != old || old.retiring || len(old.displace) != 0 {
		t.Fatal("canceled admission displaced the live holder")
	}
}

func TestPairingEvictionProtectsAProvisionalHandshake(t *testing.T) {
	ctx := context.Background()
	store := &MemoryStore{}
	client, err := New(ctx, Config{Name: "Record eviction", Store: store, Roles: []Role{Controller}, RecordCapacity: 5})
	if err != nil {
		t.Fatal(err)
	}
	var records []PairingRecord
	for i := range 5 {
		server, err := NewCredentials()
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, PairingRecord{ServerID: server.ClientID(), PSK: Secret{byte(i + 1)}})
	}
	client.credentials.Records = records
	provisional := newSession(client, ctx, func() {})
	client.sessions[provisional] = true
	resolve := client.sessionResolver(provisional, records[0].ServerID)
	if _, err = resolve(pskID(records[0].PSK), "lt", true); err != nil {
		t.Fatal(err)
	}
	if provisional.channel != nil || provisional.recordServerID != records[0].ServerID {
		t.Fatal("handshake did not protect its record before Channel exists")
	}
	// A failed re-handshake must not unprotect the still-open old session.
	if _, err = resolve("unknown", "lt", false); err == nil {
		t.Fatal("unknown re-handshake PSK was accepted")
	}
	if provisional.recordServerID != records[0].ServerID {
		t.Fatal("failed re-handshake lost old-record protection")
	}
	server, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	pairing := newSession(client, ctx, func() {})
	pairing.channel = &wire.Channel{ServerID: server.ClientID(), Category: "pr"}
	newPSK := Secret{123}
	pairing.pendingPSK = &newPSK
	if err = pairing.finalizePairing([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Records) != 5 || persisted.Records[0] != records[0] || persisted.Records[1] != records[2] || persisted.Records[4].PSK != newPSK {
		t.Fatal("evicted a protected record instead of the oldest free one")
	}
	if _, err = resolve(pskID(client.credentials.PairingPSK), "pr", false); err != nil {
		t.Fatal(err)
	}
	if provisional.recordServerID != records[0].ServerID {
		t.Fatal("selection alone released old-record protection")
	}
	provisional.commitChannel(&wire.Channel{ServerID: records[0].ServerID, Category: "pr"})
	if provisional.recordServerID != "" {
		t.Fatal("successful re-handshake did not release old-record protection")
	}
}

func TestRehandshakeSelectionKeepsTheOpenRecordProtected(t *testing.T) {
	for _, category := range []string{"pr", "sn"} {
		t.Run(category, func(t *testing.T) {
			client, err := New(context.Background(), Config{Name: "Pending rekey protection", Store: &MemoryStore{}, Roles: []Role{Controller}, RecordCapacity: 5})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 5 {
				server, err := NewCredentials()
				if err != nil {
					t.Fatal(err)
				}
				client.credentials.Records = append(client.credentials.Records, PairingRecord{ServerID: server.ClientID(), PSK: Secret{byte(i + 1)}})
			}
			records := append([]PairingRecord(nil), client.credentials.Records...)
			old := newSession(client, context.Background(), func() {})
			old.channel = &wire.Channel{ServerID: records[0].ServerID, Category: "lt"}
			client.sessions[old] = true
			resolve := client.sessionResolver(old, records[0].ServerID)
			if _, err = resolve(pskID(records[0].PSK), "lt", true); err != nil {
				t.Fatal(err)
			}
			key := client.credentials.PairingPSK
			if category == "sn" {
				key = sentinelPSK
			}
			if _, err = resolve(pskID(key), category, false); err != nil {
				t.Fatal(err)
			}
			// Selection precedes Noise message-2 generation/transmission. A write
			// may still fail or block; until commit the old transport is open.
			if old.recordServerID != records[0].ServerID {
				t.Fatal("pending rekey released the still-open old record")
			}
			server, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			pairing := newSession(client, context.Background(), func() {})
			pairing.channel = &wire.Channel{ServerID: server.ClientID(), Category: "pr"}
			newKey := Secret{124}
			pairing.pendingPSK = &newKey
			if err = pairing.finalizePairing([]byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if client.credentials.Records[0] != records[0] || client.credentials.Records[1] != records[2] {
				t.Fatal("pairing evicted a record backing an unfinished rekey")
			}
			old.commitChannel(&wire.Channel{ServerID: records[0].ServerID, Category: category})
			if old.recordServerID != "" {
				t.Fatal("committed unpaired channel retained obsolete protection")
			}
			nextServer, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			pairing.channel = &wire.Channel{ServerID: nextServer.ClientID(), Category: "pr"}
			nextKey := Secret{125}
			pairing.pendingPSK = &nextKey
			if err = pairing.finalizePairing([]byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if client.credentials.Records[0].ServerID == records[0].ServerID {
				t.Fatal("committed rekey did not release the old eviction candidate")
			}
		})
	}
}

func TestTimeSampleUsesCapturedArrivalNotActorTime(t *testing.T) {
	ctx := context.Background()
	client, err := New(ctx, Config{Name: "Clock boundary", Store: &MemoryStore{}, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, ctx, func() {})
	s.epoch = time.Now().Add(-time.Second)
	s.arrival = 900
	s.timeOutstanding = 100
	payload, _ := json.Marshal(map[string]int64{"client_transmitted": 100, "server_received": 500, "server_transmitted": 600})
	if err = s.timeSample(payload); err != nil {
		t.Fatal(err)
	}
	if s.bestAt != 900 || s.filter.ServerTime(1000) != 1050 || s.filter.Error() != 175 {
		t.Fatal("actor delay contaminated the NTP measurement")
	}
}

func TestDisplacementWaitsForRehandshakeActivation(t *testing.T) {
	// No channel is installed: attempting any application write would panic.
	// A displaced connection must wait for the first new-key server/activate.
	s := &session{awaitingActivation: true}
	if err := s.displaced("another_server"); err != nil {
		t.Fatal(err)
	}
	if s.pendingDisplacement != "another_server" || !s.awaitingActivation {
		t.Fatal("displacement lost the re-handshake barrier")
	}
}

func TestShutdownDoesNotWriteBeforeHelloOrDuringRehandshake(t *testing.T) {
	for _, hello := range []bool{false, true} {
		s := mediaSession(t)
		s.hello = hello
		s.awaitingActivation = hello
		// No transport keys or socket are installed: any write would panic.
		// Neither early shutdown nor shutdown during re-handshake may send one.
		s.shutdownGoodbye()
	}
}

func TestInitialActivationBudgetIncludesHandshakeAndIsCapped(t *testing.T) {
	for _, timeout := range []time.Duration{10 * time.Millisecond, 30 * time.Second, time.Minute} {
		client := &Client{cfg: Config{HandshakeTimeout: timeout}}
		s := newSession(client, context.Background(), func() {})
		want := min(timeout, 30*time.Second)
		if got := s.activationDeadline.Sub(s.epoch); got != want {
			t.Fatalf("timeout %s: initial budget %s, want %s", timeout, got, want)
		}
	}
}

func TestActorDoesNotRestartTheInitialActivationBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		// No data is sent: this isolates the actor's activation budget,
		// not cryptography (covered by encrypted-channel and E2E tests).
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	client := &Client{cfg: Config{HandshakeTimeout: time.Minute}}
	s := newSession(client, ctx, cancel)
	s.channel = &wire.Channel{Conn: conn}
	// Represent a handshake that has already consumed its entire budget.
	s.activationDeadline = time.Now().Add(-time.Millisecond)
	if err = s.run(); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("actor restored a fresh timeout after the handshake", err)
	}
}

func TestFirstActivationWithoutRolesUsesAnEmptySet(t *testing.T) {
	ctx := context.Background()
	client, err := New(ctx, Config{Name: "Initial activation", Store: &MemoryStore{}, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, ctx, func() {})
	s.channel = &wire.Channel{ServerID: client.ClientID(), Category: "lt"}
	s.hello = true
	s.admitted = true
	if err = s.activate([]byte(`{"activities":[]}`)); err != nil {
		t.Fatal(err)
	}
	if !s.activated || len(s.state.ActiveRoles) != 0 {
		t.Fatal("omitted first active_roles was not treated as empty")
	}
}

func TestAdmittedActivityUpdatesDoNotRunAdmissionAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := &MemoryStore{}
	client, err := New(ctx, Config{Name: "Activity updates", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, ctx, func() {})
	s.channel = &wire.Channel{ServerID: client.ClientID(), Category: "lt"}
	s.hello, s.activated, s.admitted = true, true, true
	client.active = s
	// Hold the admission lock as a deterministic barrier. An already-admitted
	// connection must not wait for this lock when changing its activities.
	client.admission.Lock()
	defer client.admission.Unlock()
	for _, rank := range []int{2, 0, 2} {
		payload := []byte(`{"activities":[]}`)
		if rank == 2 {
			payload = []byte(`{"activities":["playback"]}`)
		}
		done := make(chan error, 1)
		go func() { done <- s.activate(payload) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("activity update tried to repeat admission")
		}
		if client.active != s || s.rank != rank {
			t.Fatal("activity update displaced the holder or failed to update its rank")
		}
		persisted, err := store.Load(ctx)
		if err != nil || persisted.LastPlaybackServer != s.channel.ServerID {
			t.Fatal("activity update lost the persistent last-playback server", err)
		}
	}
}

func TestRehandshakeRestartsClockRequestsWithoutResettingTheFilter(t *testing.T) {
	client := &Client{cfg: Config{HandshakeTimeout: time.Second}}
	s := newSession(client, context.Background(), func() {})
	for _, at := range []int64{10, 20, 30} {
		s.filter.Update(100, 50, at)
	}
	if !s.filter.Ready() {
		t.Fatal("test filter did not converge")
	}
	s.timeOutstanding = 1234
	s.burstLeft = 2
	s.bestUncertainty = 1000
	s.nextBurst = time.Now().Add(time.Hour)
	s.restartTimeExchange()
	if s.timeOutstanding != 0 || s.burstLeft != 8 || s.bestUncertainty != 0 || !s.nextBurst.IsZero() {
		t.Fatal("old request survived the re-handshake")
	}
	if !s.filter.Ready() || s.filter.ServerTime(500) != 600 {
		t.Fatal("re-handshake reset established synchronization")
	}
	s.timeOutstanding = 2000
	s.arrival = 3000
	late := []byte(`{"client_transmitted":1234,"server_received":999999,"server_transmitted":999999}`)
	if err := s.timeSample(late); err != nil {
		t.Fatal("late old-key response disconnected client", err)
	}
	if s.timeOutstanding != 2000 || s.burstLeft != 8 || s.filter.ServerTime(500) != 600 {
		t.Fatal("late response changed the current exchange/filter")
	}
	current := []byte(`{"client_transmitted":2000,"server_received":2100,"server_transmitted":2200}`)
	if err := s.timeSample(current); err != nil {
		t.Fatal(err)
	}
	if s.timeOutstanding != 0 || s.burstLeft != 7 || s.bestAt != 3000 {
		t.Fatal("current response did not complete its exchange")
	}
}
