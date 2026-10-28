package mdns

import (
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type packetConn4 struct {
	*ipv4.PacketConn
	raw *net.UDPConn
}
type packetConn6 struct {
	*ipv6.PacketConn
	raw *net.UDPConn
}

func newPacketConn4(c *net.UDPConn) *packetConn4 { return &packetConn4{ipv4.NewPacketConn(c), c} }
func newPacketConn6(c *net.UDPConn) *packetConn6 { return &packetConn6{ipv6.NewPacketConn(c), c} }
