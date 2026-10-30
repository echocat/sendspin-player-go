package mdnstest

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func fixtureRR(t *testing.T, text string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(text)
	if err != nil {
		t.Fatal(err)
	}
	return rr
}

func TestIndependentDNSViewResolvesSplitPacketsScopesAndGoodbyes(t *testing.T) {
	p := &dnsPeer{iface: net.Interface{Index: 7}, cache: map[string]cachedRecord{}}
	now := time.Now()
	for _, records := range [][]string{
		{"_sendspin._tcp.local. 120 IN PTR player._sendspin._tcp.local."},
		{"player._sendspin._tcp.local. 120 IN SRV 0 0 9000 speaker.local.", "player._sendspin._tcp.local. 120 IN TXT \"path=/sendspin\" \"name=Kitchen.Speaker Ä\""},
		{"speaker.local. 120 IN AAAA fe80::1234", "speaker.local. 120 IN A 10.1.2.3"},
	} {
		m := &dns.Msg{}
		for _, text := range records {
			m.Extra = append(m.Extra, fixtureRR(t, text))
		}
		wire, err := m.Pack()
		if err != nil {
			t.Fatal(err)
		}
		var decoded dns.Msg
		if err := decoded.Unpack(wire); err != nil {
			t.Fatal(err)
		}
		p.ingest(&decoded, now)
	}
	clients, err := p.clients()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := clients["player"]
	if !ok || len(clients) != 1 || r.Port != 9000 || r.Properties["path"] != "/sendspin" || r.Properties["name"] != "Kitchen.Speaker Ä" || strings.Join(r.Addresses, ",") != "10.1.2.3,fe80::1234%7" || r.InterfaceIndex != 7 {
		t.Fatal("independent DNS resolution", clients)
	}
	goodbye := fixtureRR(t, "_sendspin._tcp.local. 0 IN PTR player._sendspin._tcp.local.")
	goodbye.Header().Class |= 0x8000 // Cache-flush must not alter record identity.
	p.ingest(&dns.Msg{Answer: []dns.RR{goodbye}}, now)
	clients, _ = p.clients()
	if len(clients) != 0 {
		t.Fatal("goodbye retained service", clients)
	}
}

func TestDNSTextPresentationEscapesPreserveUTF8AndLiteralBackslashes(t *testing.T) {
	for text, want := range map[string]string{
		`Kitchen.Speaker \195\132`: "Kitchen.Speaker Ä",
		`back\\slash\032space`:     `back\slash space`,
		`quote\"`:                  `quote"`,
		"plain UTF-8 Küche":        "plain UTF-8 Küche",
	} {
		if got := dnsTextBytes(text); got != want {
			t.Errorf("DNS TXT %q decoded as %q, want %q", text, got, want)
		}
	}
}

func TestIndependentDNSViewExpiresRecordsAndKeepsTXTOptional(t *testing.T) {
	p := &dnsPeer{iface: net.Interface{Index: 9}, cache: map[string]cachedRecord{}}
	m := &dns.Msg{}
	for _, text := range []string{
		"_sendspin._tcp.local. 1 IN PTR player._sendspin._tcp.local.",
		"player._sendspin._tcp.local. 1 IN SRV 0 0 9000 speaker.local.",
		"speaker.local. 1 IN AAAA fd00::1",
	} {
		m.Answer = append(m.Answer, fixtureRR(t, text))
	}
	p.ingest(m, time.Now())
	clients, _ := p.clients()
	if len(clients) != 1 || clients["player"].Properties["path"] != "" || clients["player"].Addresses[0] != "fd00::1" {
		t.Fatal("view must preserve missing TXT and never scope a ULA", clients)
	}
	p.ingest(&dns.Msg{}, time.Now().Add(2*time.Second))
	clients, _ = p.clients()
	if len(clients) != 0 || len(p.cache) != 0 {
		t.Fatal("expired records retained", clients)
	}
}

func TestIndependentServerPublicationDoesNotInventMissingPath(t *testing.T) {
	p := &dnsPeer{}
	p.advertise("reference0123456789", net.ParseIP("fe80::1"), 9001, map[string]string{"name": "No path"})
	m := p.announcement(120)
	wire, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var decoded dns.Msg
	if err := decoded.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Answer) != 4 || !decoded.Response || !decoded.Authoritative {
		t.Fatal("bad DNS-SD publication", &decoded)
	}
	for _, rr := range decoded.Answer {
		if txt, ok := rr.(*dns.TXT); ok && strings.Join(txt.Txt, ",") != "name=No path" {
			t.Fatal("publisher invented path", txt)
		}
	}
	for _, rr := range p.announcement(0).Answer {
		if rr.Header().Ttl != 0 {
			t.Fatal("goodbye TTL", rr)
		}
	}
}

func TestMulticastGuardRejectsHostBeforeOpeningSocket(t *testing.T) {
	t.Setenv("SENDSPIN_MDNS_TEST_NETWORK", "")
	if peer, err := newDNSPeer(net.Interface{Index: 1}, 4); err == nil || peer != nil {
		t.Fatal("host multicast was not rejected")
	}
}
