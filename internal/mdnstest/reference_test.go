//go:build e2e

package mdnstest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sendspin "github.com/echocat/sendspin-player-go"
)

type referenceServer struct {
	url, id string
	peer    *dnsPeer
	stop    func()
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func referenceControl(ctx context.Context, address string, payload map[string]any) (map[string]any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address+"/control", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reference control: %s", response.Status)
	}
	var out map[string]any
	err = json.NewDecoder(response.Body).Decode(&out)
	return out, err
}

func (r referenceServer) control(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := referenceControl(ctx, r.url, payload)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func (p *dnsPeer) observed(t *testing.T) map[string]any {
	t.Helper()
	clients, err := p.clients()
	if err != nil {
		t.Fatal("independent Go DNS peer:", err)
	}
	data, err := json.Marshal(clients)
	if err != nil {
		t.Fatal(err)
	}
	var records map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	return records
}

func startDNSPeer(t *testing.T, iface net.Interface, family int) (*dnsPeer, func()) {
	t.Helper()
	peer, err := newDNSPeer(iface, family)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if err := peer.close(); err != nil {
				t.Error("independent DNS peer:", err)
			}
		})
	}
	t.Cleanup(stop)
	return peer, stop
}

// aiosendspin supplies encrypted sessions only. This independent Go peer
// supplies the DNS view and chooses server-initiated dial targets from it.
func startMDNSReference(t *testing.T, cred sendspin.Credentials, iface net.Interface, ip net.IP, mode string, properties map[string]string) referenceServer {
	t.Helper()
	family := 4
	if ip.To4() == nil {
		family = 6
	}
	peer, stop := startDNSPeer(t, iface, family)
	psk := sendspin.Secret{111}
	settings := map[string]any{"client_id": cred.ClientID(), "psk": base64.RawURLEncoding.EncodeToString(psk[:]), "pairing": false}
	logical := referenceSuite.Reference(t, settings)
	ref := referenceServer{url: logical.URL, id: logical.ID, peer: peer, stop: stop}
	if mode == "advertise_server" {
		// Forward only TCP, not DNS: each logical official peer keeps its private
		// loopback control URL while the advertised address has the tested scope.
		port := forwardReference(t, ref.url, iface, ip)
		if properties == nil {
			properties = map[string]string{"path": "/sendspin", "name": "Discovery hint"}
		}
		peer.advertise(ref.id, ip, port, properties)
	} else if mode == "discover_clients" {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					clients, err := peer.clients()
					if err != nil {
						return
					}
					record, ok := clients[cred.ClientID()]
					if !ok || !strings.HasPrefix(record.Properties["path"], "/") {
						continue
					}
					address := record.Addresses[0]
					// aiosendspin/aiohttp consumes the scoped host directly, just as
					// socket.getaddrinfo does; do not escape '%' to '%25' here.
					endpoint := "ws://" + net.JoinHostPort(address, strconv.Itoa(record.Port)) + record.Properties["path"]
					controlCtx, stop := context.WithTimeout(ctx, 5*time.Second)
					_, err = referenceControl(controlCtx, ref.url, map[string]any{"command": "connect", "url": endpoint})
					stop()
					if err != nil && ctx.Err() == nil {
						peer.fail(err)
					}
					return
				}
			}
		}()
		t.Cleanup(func() { cancel(); <-done })
	} else {
		t.Fatalf("unknown Go DNS peer mode %q", mode)
	}
	return ref
}

func forwardReference(t *testing.T, referenceURL string, iface net.Interface, ip net.IP) int {
	t.Helper()
	target, err := url.Parse(referenceURL)
	if err != nil {
		t.Fatal(err)
	}
	address := &net.TCPAddr{IP: ip}
	network := "tcp4"
	if ip.To4() == nil {
		network, address.Zone = "tcp6", strconv.Itoa(iface.Index)
	}
	listener, err := net.ListenTCP(network, address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer func() { _ = incoming.Close() }()
				outgoing, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target.Host)
				if err != nil {
					return
				}
				defer func() { _ = outgoing.Close() }()
				closing := context.AfterFunc(ctx, func() { _ = incoming.Close(); _ = outgoing.Close() })
				defer closing()
				finished := make(chan struct{})
				go func() { _, _ = io.Copy(outgoing, incoming); _ = outgoing.(*net.TCPConn).CloseWrite(); close(finished) }()
				_, _ = io.Copy(incoming, outgoing)
				_ = incoming.Close()
				<-finished
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return listener.Addr().(*net.TCPAddr).Port
}

type referenceConnection struct {
	done chan struct{}
	err  error
}

func connectReference(t *testing.T, client *sendspin.Client, ref referenceServer) *referenceConnection {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	connection := &referenceConnection{done: make(chan struct{})}
	go func() {
		connection.err = client.Connect(ctx, strings.Replace(ref.url, "http://", "ws://", 1)+"/sendspin")
		close(connection.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-connection.done:
			if !errors.Is(connection.err, context.Canceled) {
				t.Error(connection.err)
			}
		case <-time.After(5 * time.Second):
			t.Error("reference connection did not stop")
		}
	})
	return connection
}
