package mdns

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/windows"
)

func TestWindowsPacketInfoParserRejectsMissingMalformedAndAmbiguousScope(t *testing.T) {
	control := packetInfoControl(windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4, 7)
	if index, err := packetInfoIndex(control, windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4); err != nil || index != 7 {
		t.Fatal(index, err)
	}
	for _, fault := range []string{"length", "truncated", "zero", "ambiguous"} {
		b := bytes.Clone(control)
		switch fault {
		case "length":
			for i := range controlWordSize {
				b[i] = 255
			}
		case "truncated":
			if controlWordSize == 8 {
				binary.LittleEndian.PutUint64(b, uint64(controlHeaderSize))
			} else {
				binary.LittleEndian.PutUint32(b, uint32(controlHeaderSize))
			}
		case "zero":
			b = packetInfoControl(windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4, 0)
		case "ambiguous":
			b = append(b, packetInfoControl(windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4, 9)...)
		}
		if _, err := packetInfoIndex(b, windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4); err == nil {
			t.Fatal("invalid packet info accepted", fault)
		}
	}
	if index, err := packetInfoIndex(nil, windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4); err != nil || index != 0 {
		t.Fatal("invented missing interface", index, err)
	}
}

// Loopback UDP only: native packet metadata is tested without joining multicast
// groups, touching a physical interface, or changing host network configuration.
func TestWindowsUDPInterfaceMetadataAndScopedReply(t *testing.T) {
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			ip := net.IPv4(127, 0, 0, 1)
			if network == "udp6" {
				ip = net.IPv6loopback
			}
			raw, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			if err = raw.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			client, err := net.DialUDP(network, nil, raw.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if err = client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var data [32]byte
			if network == "udp4" {
				c := newPacketConn4(raw)
				if err = c.SetControlMessage(ipv4.FlagInterface, true); err != nil {
					t.Fatal(err)
				}
				if _, err = client.Write([]byte("scope")); err != nil {
					t.Fatal(err)
				}
				n, cm, from, e := c.ReadFrom(data[:])
				if e != nil || n != 5 || cm == nil || cm.IfIndex <= 0 {
					t.Fatal("missing native IPv4 receiving index", n, cm, e)
				}
				if _, err = c.WriteTo(data[:n], cm, from); err != nil {
					t.Fatal("scoped IPv4 reply", err)
				}
			} else {
				c := newPacketConn6(raw)
				if err = c.SetControlMessage(ipv6.FlagInterface, true); err != nil {
					t.Fatal(err)
				}
				if _, err = client.Write([]byte("scope")); err != nil {
					t.Fatal(err)
				}
				n, cm, from, e := c.ReadFrom(data[:])
				if e != nil || n != 5 || cm == nil || cm.IfIndex <= 0 {
					t.Fatal("missing native IPv6 receiving index", n, cm, e)
				}
				if _, err = c.WriteTo(data[:n], cm, from); err != nil {
					t.Fatal("scoped IPv6 reply", err)
				}
			}
			n, err := client.Read(data[:])
			if err != nil || string(data[:n]) != "scope" {
				t.Fatal("scoped reply did not reach sender", err)
			}
		})
	}
}
