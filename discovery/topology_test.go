package discovery

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	zeroconf "github.com/echocat/sendspin-player-go/internal/mdns"
)

func TestIPv6LinkLocalUsesReceivingScopeAndEscapesTheRequestTarget(t *testing.T) {
	e := &zeroconf.ServiceEntry{Port: 8927, Text: []string{"path=/sendspin%2Froom?q=a%2Fb", "zone=attacker"}, AddrIPv6: []net.IP{net.ParseIP("fe80::123"), net.ParseIP("fd00::123")}}
	got := scopedEndpoints(e, "7")
	if len(got) != 2 || got[0].URL != "ws://[fe80::123%257]:8927/sendspin%2Froom?q=a%2Fb" || strings.Contains(got[1].URL, "%25") {
		t.Fatal(got)
	}
	u, err := url.Parse(got[0].URL)
	if err != nil || u.Hostname() != "fe80::123%7" || u.RequestURI() != "/sendspin%2Froom?q=a%2Fb" {
		t.Fatal(u, err)
	}
	if unscoped := endpoints(e); len(unscoped) != 1 || strings.Contains(unscoped[0].URL, "fe80") {
		t.Fatal("invented a missing scope", unscoped)
	}
}

func TestInterfaceSnapshotsKeepExplicitSelectionAndDetectAddressChanges(t *testing.T) {
	flags := net.FlagUp | net.FlagMulticast
	a, b := net.Interface{Index: 7, Name: "one", Flags: flags}, net.Interface{Index: 9, Name: "two", Flags: flags}
	address := "fe80::1"
	ips := func(net.Interface) ([]net.IP, error) { return []net.IP{net.ParseIP(address)}, nil }
	one, err := snapshotInterfaces([]net.Interface{a, b}, []net.Interface{a}, ips)
	if err != nil || len(one.interfaces) != 1 || one.interfaces[0].Index != 7 {
		t.Fatal(one, err)
	}
	address = "fe80::2"
	two, err := snapshotInterfaces([]net.Interface{b, a}, []net.Interface{a}, ips)
	if err != nil || one.signature == two.signature {
		t.Fatal("address change invisible")
	}
	changed := a
	changed.MTU = 1400
	changed.HardwareAddr = net.HardwareAddr{2, 0, 0, 0, 0, 7}
	three, err := snapshotInterfaces([]net.Interface{changed}, []net.Interface{a}, ips)
	if err != nil || three.signature == two.signature {
		t.Fatal("interface replacement/MTU change invisible")
	}
	reused := a
	reused.Name = "replacement"
	none, err := snapshotInterfaces([]net.Interface{reused, b}, []net.Interface{a}, ips)
	if err != nil || len(none.interfaces) != 0 {
		t.Fatal("selection broadened or reused an unrelated interface", none, err)
	}
}

func TestTopologyWatcherJoinsOldScopesBeforeInterfaceReappearance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var phase, active atomic.Int32
	stopped := make(chan int, 8)
	events := make(chan Endpoint, 8)
	snapshot := func() (networkSnapshot, error) {
		switch phase.Load() {
		case 0:
			return networkSnapshot{[]net.Interface{{Index: 7, Name: "one"}}, "one"}, nil
		case 1:
			return networkSnapshot{}, nil
		case 2:
			return networkSnapshot{[]net.Interface{{Index: 9, Name: "two"}}, "two"}, nil
		default:
			return networkSnapshot{[]net.Interface{{Index: 9, Name: "two"}}, "address-change"}, nil
		}
	}
	lookup := func(ctx context.Context, family zeroconf.IPType, interfaces []net.Interface, entries chan<- *zeroconf.ServiceEntry) error {
		if len(interfaces) != 1 {
			t.Error("mixed resolver scope")
		}
		if family == zeroconf.IPv4 {
			return errors.New("IPv4 unavailable")
		}
		if active.Add(1) != 1 {
			t.Error("old resolver survived topology restart")
		}
		defer active.Add(-1)
		e := &zeroconf.ServiceEntry{Port: 8927, Text: []string{"path=/sendspin"}, AddrIPv6: []net.IP{net.ParseIP("fe80::123")}}
		select {
		case entries <- e:
		case <-ctx.Done():
		}
		<-ctx.Done()
		entries <- e // must be drained, never emitted after restart
		stopped <- interfaces[0].Index
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- watchInterfaces(ctx, snapshot, lookup, func(e Endpoint) { events <- e }, time.Millisecond)
	}()
	next := func(want string) {
		t.Helper()
		select {
		case e := <-events:
			if !strings.Contains(e.URL, want) {
				t.Fatal(e)
			}
		case <-ctx.Done():
			t.Fatal("no scoped discovery", ctx.Err())
		}
	}
	waitStop := func(want int) {
		t.Helper()
		select {
		case got := <-stopped:
			if got != want {
				t.Fatal(got, want)
			}
		case <-ctx.Done():
			t.Fatal("old resolver did not close")
		}
	}
	next("%257")
	phase.Store(1)
	waitStop(7)
	phase.Store(2)
	next("%259")
	phase.Store(3)
	waitStop(9)
	next("%259")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || active.Load() != 0 {
		t.Fatal("watcher returned before cleanup", err, active.Load())
	}
}

func TestLastInterfaceFailureIsReconciledBeforeReturningSetupError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var phase atomic.Int32
	failed := make(chan struct{})
	events := make(chan Endpoint, 4)
	snapshot := func() (networkSnapshot, error) {
		if phase.Load() == 0 {
			return networkSnapshot{[]net.Interface{{Index: 7, Name: "old"}}, "old"}, nil
		}
		return networkSnapshot{[]net.Interface{{Index: 9, Name: "new"}}, "new"}, nil
	}
	lookup := func(ctx context.Context, family zeroconf.IPType, ifaces []net.Interface, entries chan<- *zeroconf.ServiceEntry) error {
		if family == zeroconf.IPv4 {
			return errors.New("no IPv4")
		}
		entry := &zeroconf.ServiceEntry{Port: 8927, Text: []string{"path=/sendspin"}, AddrIPv6: []net.IP{net.ParseIP("fe80::1")}}
		select {
		case entries <- entry:
		case <-ctx.Done():
			return ctx.Err()
		}
		if ifaces[0].Index == 7 {
			select {
			case <-failed:
				return errors.New("interface removed")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- watchInterfaces(ctx, snapshot, lookup, func(e Endpoint) { events <- e }, time.Hour) }()
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal("old interface not started")
	}
	phase.Store(1)
	close(failed)
	select {
	case e := <-events:
		if !strings.Contains(e.URL, "%259") {
			t.Fatal(e)
		}
	case err := <-done:
		t.Fatal("transient topology loss treated as permanent setup error", err)
	case <-ctx.Done():
		t.Fatal("replacement interface not resolved")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
