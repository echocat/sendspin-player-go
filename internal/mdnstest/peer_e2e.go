//go:build e2e

package mdnstest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

func (p *dnsPeer) close() error {
	p.stop()
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func runObserver() error {
	if err := containerOnly(); err != nil {
		return err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	var selected []net.Interface
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 && iface.Flags&net.FlagMulticast != 0 {
			selected = append(selected, iface)
		}
	}
	if len(selected) != 1 {
		return fmt.Errorf("observer needs exactly one isolated interface, got %d", len(selected))
	}
	p, err := newDNSPeer(selected[0], 6)
	if err != nil {
		return err
	}
	defer func() { _ = p.close() }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		clients, err := p.clients()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"clients": clients})
	})
	server := &http.Server{Addr: "0.0.0.0:8081", Handler: mux, ReadHeaderTimeout: time.Second}
	listener, err := net.Listen("tcp4", server.Addr)
	if err != nil {
		return err
	}
	fmt.Println("[mdns-observer] ready")
	return server.Serve(listener)
}
