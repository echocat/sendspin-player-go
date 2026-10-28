package discovery

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	zeroconf "github.com/echocat/sendspin-player-go/internal/mdns"
)

func TestEndpointParsing(t *testing.T) {
	entry := &zeroconf.ServiceEntry{ServiceRecord: zeroconf.ServiceRecord{Instance: "test"}, Port: 8927, Text: []string{"path=/sendspin", "name=server"}, AddrIPv4: []net.IP{net.ParseIP("192.0.2.1")}, AddrIPv6: []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("fe80::1")}}
	e := endpoints(entry)
	if len(e) != 2 || e[0].URL != "ws://192.0.2.1:8927/sendspin" || e[1].URL != "ws://[2001:db8::1]:8927/sendspin" || e[0].Name != "server" {
		t.Fatal(e)
	}
	entry.Text = []string{"name=missing path"}
	if len(endpoints(entry)) != 0 {
		t.Fatal("accepted missing path")
	}
	for _, path := range []string{"", "sendspin", "//evil.example/path", "/x\nInjected: yes"} {
		if validPath(path) {
			t.Fatal("accepted", path)
		}
	}
	if !validPath("/sendspin?zone=1") {
		t.Fatal("rejected valid endpoint")
	}
}

func TestTXTExactRequestTargetAndFirstCaseInsensitiveKey(t *testing.T) {
	entry := &zeroconf.ServiceEntry{ServiceRecord: zeroconf.ServiceRecord{Instance: "fallback"}, Port: 8927, Text: []string{"PATH=/sendspin%2Fzone?x=a%2Fb", "path=/wrong", "NAME=Küche", "name=wrong"}, AddrIPv4: []net.IP{nil, {1, 2}, net.IPv4bcast, net.ParseIP("169.254.10.20")}}
	found := endpoints(entry)
	if len(found) != 1 || found[0].Name != "Küche" || found[0].URL != "ws://169.254.10.20:8927/sendspin%2Fzone?x=a%2Fb" {
		t.Fatal(found)
	}
	if validPath("/sendspin#fragment") {
		t.Fatal("accepted a URI fragment")
	}
	if !validPath("/sendspin%23literal") {
		t.Fatal("rejected encoded path character")
	}
	entry.Text = []string{"PATH", "path=/must-not-override-a-flag"}
	if len(endpoints(entry)) != 0 {
		t.Fatal("key-only TXT field did not take precedence over its duplicate")
	}
}

func TestAdvertisementTXTBoundsAndOptionalLongName(t *testing.T) {
	service := ClientService{Instance: "stable-id", Name: strings.Repeat("ü", 200), Port: 8928, Path: "/sendspin"}
	text, err := serviceText(service)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(text, []string{"path=/sendspin"}) {
		t.Fatal("overlong optional name was not omitted", text)
	}
	service.Path = "/" + strings.Repeat("x", 251)
	if _, err = serviceText(service); err == nil {
		t.Fatal("accepted overlong required TXT path")
	}
	service.Path = "/sendspin"
	service.Instance = strings.Repeat("x", 64)
	if _, err = serviceText(service); err == nil {
		t.Fatal("accepted overlong DNS-SD label")
	}
}

func TestBrowseCanceledBeforeSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Browse(ctx, nil, func(Endpoint) { t.Error("callback after cancellation") }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestBrowseCancellationDrainsSubscribersAndWaitsForCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var cleaned atomic.Int32
	entry := &zeroconf.ServiceEntry{Port: 8927, Text: []string{"path=/sendspin"}, AddrIPv4: []net.IP{net.ParseIP("192.0.2.1")}}
	lookup := func(ctx context.Context, _ zeroconf.IPType, _ []net.Interface, entries chan<- *zeroconf.ServiceEntry) error {
		defer close(entries)
		select {
		case entries <- entry:
		case <-ctx.Done():
		}
		<-ctx.Done()
		// Model the upstream mainloop's unconditional subscriber send. The
		// wrapper must keep draining it, not just return ctx.Err immediately.
		entries <- entry
		cleaned.Add(1)
		return nil
	}
	err := browseServices(ctx, nil, func(Endpoint) { cancel() }, lookup)
	if !errors.Is(err, context.Canceled) || cleaned.Load() != 2 {
		t.Fatal("cancellation returned before both families closed", err, cleaned.Load())
	}
}

func TestBrowseKeepsWorkingFamilyAndCleansUpAfterCallbackPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var cleaned atomic.Bool
	entry := &zeroconf.ServiceEntry{Port: 8927, Text: []string{"path=/sendspin"}, AddrIPv4: []net.IP{net.ParseIP("192.0.2.1")}}
	lookup := func(ctx context.Context, family zeroconf.IPType, _ []net.Interface, entries chan<- *zeroconf.ServiceEntry) error {
		if family == zeroconf.IPv6 {
			return errors.New("IPv6 unavailable")
		}
		defer close(entries)
		entries <- entry
		<-ctx.Done()
		entries <- entry
		cleaned.Store(true)
		return nil
	}
	defer func() {
		if recover() == nil {
			t.Error("callback panic was swallowed")
		}
		if !cleaned.Load() {
			t.Error("panic leaked the working resolver")
		}
	}()
	_ = browseServices(ctx, nil, func(Endpoint) { panic("test callback") }, lookup)
}
