package mdnstest

// This peer deliberately shares only the DNS wire codec with the SDK. It does
// not import discovery or internal/mdns: queries, cache, DNS-SD resolution and
// advertisements are implemented independently for the test's external view.
import (
	"context"
	"errors"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const clientService = "_sendspin._tcp.local."
const serverService = "_sendspin-server._tcp.local."

type observedClient struct {
	Port           int               `json:"port"`
	Properties     map[string]string `json:"properties"`
	Addresses      []string          `json:"addresses"`
	InterfaceIndex int               `json:"interface_index"`
	Hostname       string            `json:"hostname"`
}

type cachedRecord struct {
	rr      dns.RR
	expires time.Time
}

type dnsPeer struct {
	conn    *net.UDPConn
	iface   net.Interface
	group   *net.UDPAddr
	read    func([]byte) (int, int, error)
	mu      sync.Mutex
	cache   map[string]cachedRecord
	publish []dns.RR
	done    chan struct{}
	stop    context.CancelFunc
	err     error
}

func containerOnly() error {
	if os.Getenv("SENDSPIN_MDNS_TEST_NETWORK") != "internal" {
		return errors.New("refusing multicast outside the TestMain private network")
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return nil
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return nil
	}
	return errors.New("refusing multicast outside a Linux container")
}

func newDNSPeer(iface net.Interface, family int) (*dnsPeer, error) {
	if err := containerOnly(); err != nil {
		return nil, err
	}
	network, address, group := "udp4", "0.0.0.0:5353", "224.0.0.251"
	if family == 6 {
		network, address, group = "udp6", "[::]:5353", "ff02::fb"
	}
	lc := net.ListenConfig{Control: multicastSocketControl}
	pc, err := lc.ListenPacket(context.Background(), network, address)
	if err != nil {
		return nil, err
	}
	p := &dnsPeer{conn: pc.(*net.UDPConn), iface: iface, group: &net.UDPAddr{IP: net.ParseIP(group), Port: 5353}, cache: map[string]cachedRecord{}, done: make(chan struct{})}
	if family == 6 {
		// Interface names may be reused after hotplug. Numeric zones avoid
		// net's cached name-to-index mapping from a removed generation.
		p.group.Zone = strconv.Itoa(iface.Index)
		c := ipv6.NewPacketConn(pc)
		for _, configure := range []func() error{
			func() error { return c.SetMulticastInterface(&iface) },
			func() error { return c.SetMulticastHopLimit(255) },
			func() error { return c.SetMulticastLoopback(true) },
			func() error { return c.SetControlMessage(ipv6.FlagInterface, true) },
			func() error { return c.JoinGroup(&iface, p.group) },
		} {
			if err = configure(); err != nil {
				_ = pc.Close()
				return nil, err
			}
		}
		p.read = func(b []byte) (int, int, error) {
			n, cm, _, err := c.ReadFrom(b)
			index := 0
			if cm != nil {
				index = cm.IfIndex
			}
			return n, index, err
		}
	} else {
		c := ipv4.NewPacketConn(pc)
		for _, configure := range []func() error{
			func() error { return c.SetMulticastInterface(&iface) },
			func() error { return c.SetMulticastTTL(255) },
			func() error { return c.SetMulticastLoopback(true) },
			func() error { return c.SetControlMessage(ipv4.FlagInterface, true) },
			func() error { return c.JoinGroup(&iface, p.group) },
		} {
			if err = configure(); err != nil {
				_ = pc.Close()
				return nil, err
			}
		}
		p.read = func(b []byte) (int, int, error) {
			n, cm, _, err := c.ReadFrom(b)
			index := 0
			if cm != nil {
				index = cm.IfIndex
			}
			return n, index, err
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	p.stop = stop
	go p.run(ctx)
	return p, nil
}

func (p *dnsPeer) send(m *dns.Msg) error {
	b, err := m.Pack()
	if err != nil {
		return err
	}
	_, err = p.conn.WriteToUDP(b, p.group)
	return err
}

func (p *dnsPeer) announcement(ttl uint32) *dns.Msg {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true, Authoritative: true}}
	for _, record := range p.publish {
		rr := dns.Copy(record)
		rr.Header().Ttl = ttl
		m.Answer = append(m.Answer, rr)
	}
	return m
}

func (p *dnsPeer) run(ctx context.Context) {
	defer close(p.done)
	defer func() { _ = p.conn.Close() }()
	defer func() {
		if m := p.announcement(0); len(m.Answer) > 0 {
			_ = p.send(m)
		}
	}()
	b := make([]byte, 65535)
	nextQuery := time.Time{}
	for ctx.Err() == nil {
		if time.Now().After(nextQuery) {
			q := &dns.Msg{Question: []dns.Question{{Name: clientService, Qtype: dns.TypePTR, Qclass: dns.ClassINET}}}
			p.mu.Lock()
			for _, record := range p.cache {
				switch rr := record.rr.(type) {
				case *dns.PTR:
					q.Question = append(q.Question, dns.Question{Name: rr.Ptr, Qtype: dns.TypeSRV, Qclass: dns.ClassINET}, dns.Question{Name: rr.Ptr, Qtype: dns.TypeTXT, Qclass: dns.ClassINET})
				case *dns.SRV:
					q.Question = append(q.Question, dns.Question{Name: rr.Target, Qtype: dns.TypeA, Qclass: dns.ClassINET}, dns.Question{Name: rr.Target, Qtype: dns.TypeAAAA, Qclass: dns.ClassINET})
				}
			}
			p.mu.Unlock()
			if err := p.send(q); err != nil {
				p.fail(err)
				return
			}
			if m := p.announcement(120); len(m.Answer) != 0 {
				if err := p.send(m); err != nil {
					p.fail(err)
					return
				}
			}
			nextQuery = time.Now().Add(250 * time.Millisecond)
		}
		_ = p.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		n, index, err := p.read(b)
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Timeout() {
				continue
			}
			if ctx.Err() == nil {
				p.fail(err)
			}
			return
		}
		if index != p.iface.Index {
			continue
		}
		var m dns.Msg
		if m.Unpack(b[:n]) != nil {
			continue
		}
		if m.Response {
			p.ingest(&m, time.Now())
		} else {
			for _, question := range m.Question {
				p.mu.Lock()
				matches := false
				for _, rr := range p.publish {
					matches = matches || strings.EqualFold(rr.Header().Name, question.Name)
				}
				p.mu.Unlock()
				if matches {
					if err := p.send(p.announcement(120)); err != nil {
						p.fail(err)
						return
					}
					break
				}
			}
		}
	}
}

func (p *dnsPeer) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *dnsPeer) ingest(m *dns.Msg, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, rr := range append(append(append([]dns.RR{}, m.Answer...), m.Ns...), m.Extra...) {
		if rr.Header().Class&0x7fff != dns.ClassINET {
			continue
		}
		canonical := dns.Copy(rr)
		canonical.Header().Ttl = 0
		canonical.Header().Class = dns.ClassINET
		key := canonical.String()
		if rr.Header().Ttl == 0 {
			delete(p.cache, key)
		} else {
			p.cache[key] = cachedRecord{dns.Copy(rr), now.Add(time.Duration(rr.Header().Ttl) * time.Second)}
		}
	}
	for key, record := range p.cache {
		if !now.Before(record.expires) {
			delete(p.cache, key)
		}
	}
}

func (p *dnsPeer) clients() (map[string]observedClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := map[string]observedClient{}
	for _, cached := range p.cache {
		ptr, ok := cached.rr.(*dns.PTR)
		if !ok || !strings.EqualFold(ptr.Hdr.Name, clientService) || time.Now().After(cached.expires) {
			continue
		}
		r := observedClient{Properties: map[string]string{}, InterfaceIndex: p.iface.Index}
		for _, cached := range p.cache {
			if time.Now().After(cached.expires) || !strings.EqualFold(cached.rr.Header().Name, ptr.Ptr) {
				continue
			}
			switch rr := cached.rr.(type) {
			case *dns.SRV:
				r.Port, r.Hostname = int(rr.Port), rr.Target
			case *dns.TXT:
				for _, txt := range rr.Txt {
					key, value, _ := strings.Cut(dnsTextBytes(txt), "=")
					r.Properties[key] = value
				}
			}
		}
		for _, cached := range p.cache {
			if time.Now().After(cached.expires) || !strings.EqualFold(cached.rr.Header().Name, r.Hostname) {
				continue
			}
			var ip net.IP
			switch rr := cached.rr.(type) {
			case *dns.A:
				ip = rr.A
			case *dns.AAAA:
				ip = rr.AAAA
			}
			if ip != nil {
				address := ip.String()
				if ip.To4() == nil && ip.IsLinkLocalUnicast() {
					address += "%" + strconv.Itoa(p.iface.Index)
				}
				r.Addresses = append(r.Addresses, address)
			}
		}
		sort.Strings(r.Addresses)
		if r.Port != 0 && len(r.Addresses) > 0 {
			result[strings.TrimSuffix(ptr.Ptr, "."+clientService)] = r
		}
	}
	return result, p.err
}

// miekg/dns represents TXT wire bytes using DNS presentation escapes. Decode
// those bytes before interpreting UTF-8 DNS-SD values (not with Go/JSON escapes).
func dnsTextBytes(text string) string {
	var result strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 == len(text) {
			result.WriteByte(text[i])
			continue
		}
		if i+3 < len(text) && text[i+1] >= '0' && text[i+1] <= '9' && text[i+2] >= '0' && text[i+2] <= '9' && text[i+3] >= '0' && text[i+3] <= '9' {
			value := int(text[i+1]-'0')*100 + int(text[i+2]-'0')*10 + int(text[i+3]-'0')
			if value <= 255 {
				result.WriteByte(byte(value))
				i += 3
				continue
			}
		}
		i++
		result.WriteByte(text[i])
	}
	return result.String()
}

func (p *dnsPeer) advertise(id string, ip net.IP, port int, properties map[string]string) {
	instance, host := id+"."+serverService, "reference-"+id[:min(len(id), 12)]+".local."
	hdr := func(name string, kind uint16) dns.RR_Header {
		return dns.RR_Header{Name: name, Rrtype: kind, Class: dns.ClassINET, Ttl: 120}
	}
	records := []dns.RR{
		&dns.PTR{Hdr: hdr(serverService, dns.TypePTR), Ptr: instance},
		&dns.SRV{Hdr: hdr(instance, dns.TypeSRV), Port: uint16(port), Target: host},
	}
	text := []string{}
	for key, value := range properties {
		text = append(text, key+"="+value)
	}
	sort.Strings(text)
	records = append(records, &dns.TXT{Hdr: hdr(instance, dns.TypeTXT), Txt: text})
	if ip.To4() != nil {
		records = append(records, &dns.A{Hdr: hdr(host, dns.TypeA), A: ip.To4()})
	} else {
		records = append(records, &dns.AAAA{Hdr: hdr(host, dns.TypeAAAA), AAAA: ip})
	}
	p.mu.Lock()
	p.publish = records
	p.mu.Unlock()
}
