package mdns

import (
	"net"
	"testing"
)

func TestForeignAndUnknownInterfacesAreRejectedBeforeDNSParsing(t *testing.T) {
	server := &Server{ifaces: []net.Interface{{Index: 7, Name: "allowed"}}}
	for _, index := range []int{0, -1, 9} {
		if scopeAllowed(index, server.ifaces) {
			t.Fatal("foreign/unknown receiving interface accepted", index)
		}
		if err := server.parsePacket([]byte("malformed foreign DNS"), index, &net.UDPAddr{}); err != nil {
			t.Fatal("processed foreign bytes before scope filter", index, err)
		}
	}
	if !scopeAllowed(7, server.ifaces) {
		t.Fatal("selected interface rejected")
	}
	if err := server.parsePacket([]byte("malformed local DNS"), 7, &net.UDPAddr{}); err == nil {
		t.Fatal("selected packet did not reach DNS validation")
	}
	if scopeAllowed(7, nil) {
		t.Fatal("empty selection guessed a scope")
	}
}
