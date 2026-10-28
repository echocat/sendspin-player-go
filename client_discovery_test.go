package sendspin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/discovery"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func TestListenerAddressSelectionDoesNotPublishUnboundAddresses(t *testing.T) {
	interfaces := []net.Interface{{Index: 1, Name: "lan", Flags: net.FlagUp | net.FlagMulticast}, {Index: 2, Name: "vpn", Flags: net.FlagUp | net.FlagMulticast}}
	addresses := func(iface net.Interface) ([]net.IP, error) {
		if iface.Name == "lan" {
			return []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("2001:db8::10")}, nil
		}
		return []net.IP{net.ParseIP("198.51.100.10")}, nil
	}
	for _, tc := range []struct {
		name, address string
		v4, v6        bool
		count, ifaces int
	}{
		{"specific-v4", "192.0.2.10", true, false, 1, 1},
		{"wildcard-v4", "0.0.0.0", true, false, 2, 2},
		{"wildcard-v6-only", "::", false, true, 1, 1},
		{"wildcard-dual-stack", "::", true, true, 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, err := selectListenerAddresses(&net.TCPAddr{IP: net.ParseIP(tc.address)}, tc.v4, tc.v6, interfaces, addresses)
			if err != nil {
				t.Fatal(err)
			}
			if len(service.Addresses) != tc.count || len(service.Interfaces) != tc.ifaces {
				t.Fatal(service)
			}
		})
	}
}

func TestListenerPublicationRefreshPolicyDoesNotBroadenSocketBindings(t *testing.T) {
	iface := net.Interface{Index: 7, Name: "lan", Flags: net.FlagUp | net.FlagMulticast}
	initial := discovery.ClientService{Interfaces: []net.Interface{iface}, Addresses: []net.IP{net.ParseIP("192.0.2.7")}}
	for _, tc := range []struct {
		ip, zone                string
		v4, v6                  bool
		version                 int
		fixedIP, fixedInterface bool
	}{
		{"0.0.0.0", "", true, false, 4, false, false},
		{"::", "", false, true, 6, false, false},
		{"::", "", true, true, 0, false, false},
		{"192.0.2.7", "", true, false, 4, true, false},
		{"fe80::7", "7", false, true, 6, true, true},
	} {
		address := &net.TCPAddr{IP: net.ParseIP(tc.ip), Zone: tc.zone}
		got := listenerPublicationPolicy(address, tc.v4, tc.v6, initial)
		if got.IPVersion != tc.version || (len(got.Addresses) > 0) != tc.fixedIP || (len(got.Interfaces) > 0) != tc.fixedInterface {
			t.Fatal("listener boundary changed", tc, got)
		}
		if tc.fixedIP {
			address.IP[0] ^= 1
			if got.Addresses[0].Equal(address.IP) {
				t.Fatal("retained mutable listener address")
			}
		}
	}
}

func TestServeClosesAcceptedListenerOnStartupFailure(t *testing.T) {
	client, err := New(context.Background(), Config{Name: "Startup failure", Store: &MemoryStore{}, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	// Loopback is intentionally not announced onto another interface. Failure
	// occurs before joining a multicast group, so ordinary tests touch no LAN.
	if err = client.Serve(context.Background(), listener); err == nil {
		t.Fatal("loopback discovery unexpectedly succeeded")
	}
	_ = listener.SetDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err = listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("startup failure did not close owned listener", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	listener2, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener2.Close() }()
	if err = client.Serve(ctx, listener2); !errors.Is(err, context.Canceled) {
		t.Fatal("failed start retained the inbound mode", err)
	}
}

func TestServeWaitsOnlyForItsOwnHTTPHandlers(t *testing.T) {
	client, err := New(context.Background(), Config{Name: "Handler ownership", Store: &MemoryStore{}, Roles: []Role{Controller}, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	external := httptest.NewServer(client)
	defer external.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, _, err := websocket.Dial(ctx, "ws"+external.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.CloseNow() }()
	messageType, raw, err := peer.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		t.Fatal("external handshake did not begin", err)
	}
	message, err := protocol.Parse(raw)
	if err != nil || message.Type != "client/init" {
		t.Fatal(message, err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Serve(serveCtx, listener) }()
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		stop()
		t.Fatal("managed HTTP server did not start", err)
	}
	_ = response.Body.Close()
	stop()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve waited for an unrelated upgraded connection")
	}
	client.mu.Lock()
	remaining := len(client.sessions)
	client.mu.Unlock()
	if remaining != 1 {
		t.Fatal("managed server canceled an external HTTP handler")
	}
	if err = client.Connect(ctx, "ws://127.0.0.1:1/sendspin"); !errors.Is(err, ErrConnectionMode) {
		t.Fatal("external handler's mode reservation was lost", err)
	}
}

func TestRunRejectsPermanentEndpointErrorsWithoutBackoff(t *testing.T) {
	client, err := New(context.Background(), Config{Name: "Endpoint validation", Store: &MemoryStore{}, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err = client.Run(ctx, "https://not-a-sendspin-endpoint.invalid"); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("invalid configuration was retried as a transient network error", err)
	}
}
