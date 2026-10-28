package discovery

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/slf4g/level"
	"github.com/echocat/slf4g/testing/recording"
)

type testRegistration struct {
	closed atomic.Int32
	stop   func()
}

func (r *testRegistration) Shutdown() {
	if r.closed.Add(1) != 1 {
		panic("registration closed twice")
	}
	if r.stop != nil {
		r.stop()
	}
}
func advertisementService() ClientService {
	return ClientService{Instance: "fixture", Name: "private-name", Port: 8928, Path: "/sendspin", Logger: recording.NewLogger()}
}
func testBinding(signature string) advertisedBinding {
	return advertisedBinding{iface: net.Interface{Index: 7, Name: "lan"}, addresses: []string{"192.0.2.7"}, signature: signature}
}

func TestAdvertisementPlansKeepFamilyAddressAndInterfaceRestrictions(t *testing.T) {
	flags := net.FlagUp | net.FlagMulticast
	lan, vpn := net.Interface{Index: 7, Name: "lan", Flags: flags}, net.Interface{Index: 9, Name: "vpn", Flags: flags}
	ips := func(i net.Interface) ([]net.IP, error) {
		if i.Index == 7 {
			return []net.IP{net.ParseIP("192.0.2.7"), net.ParseIP("fe80::7"), net.IPv4bcast, nil}, nil
		}
		return []net.IP{net.ParseIP("198.51.100.9"), net.ParseIP("fd00::9")}, nil
	}
	for _, version := range []int{0, 4, 6} {
		s := advertisementService()
		s.IPVersion = version
		plan, err := advertisementBindings(s, []net.Interface{lan, vpn}, ips)
		if err != nil || len(plan) != 2 {
			t.Fatal(plan, err)
		}
		for _, b := range plan {
			for _, raw := range b.addresses {
				ip := net.ParseIP(raw)
				if (version == 4 && ip.To4() == nil) || (version == 6 && ip.To4() != nil) {
					t.Fatal("published an unreachable socket family", version, raw)
				}
			}
		}
	}
	s := advertisementService()
	s.Interfaces = []net.Interface{lan}
	s.Addresses = []net.IP{net.ParseIP("192.0.2.7")}
	s.IPVersion = 4
	plan, err := advertisementBindings(s, []net.Interface{vpn, lan}, ips)
	if err != nil || len(plan) != 1 || len(plan["7/lan"].addresses) != 1 {
		t.Fatal(plan, err)
	}
	lan.Name = "replacement"
	plan, err = advertisementBindings(s, []net.Interface{lan, vpn}, ips)
	if err != nil || len(plan) != 0 {
		t.Fatal("reused interface index broadened selection", plan, err)
	}
	s.IPVersion = 5
	if _, err = serviceText(s); err == nil {
		t.Fatal("accepted unknown IP family")
	}
}

func TestAdvertisementCopiesMutableCallerInputs(t *testing.T) {
	s := advertisementService()
	s.Interfaces = []net.Interface{{Index: 7, Name: "lan", HardwareAddr: net.HardwareAddr{1, 2, 3}}}
	s.Addresses = []net.IP{net.ParseIP("192.0.2.7")}
	copy := copyService(s)
	s.Interfaces[0].Name = "changed"
	s.Interfaces[0].HardwareAddr[0] = 9
	s.Addresses[0][0] ^= 1
	if copy.Interfaces[0].Name != "lan" || copy.Interfaces[0].HardwareAddr[0] != 1 || !copy.Addresses[0].Equal(net.ParseIP("192.0.2.7")) {
		t.Fatal("caller can mutate live publication policy")
	}
}

func TestPublicationWithdrawsBeforeReplacementAndPreservesUnchangedBindings(t *testing.T) {
	var sequence []string
	p := &publication{service: advertisementService(), live: map[string]liveBinding{}, register: func(_ ClientService, b advertisedBinding) (registration, error) {
		sequence = append(sequence, "start:"+b.signature)
		return &testRegistration{stop: func() { sequence = append(sequence, "stop:"+b.signature) }}, nil
	}}
	if err := p.refresh(context.Background(), map[string]advertisedBinding{"a": testBinding("old"), "b": testBinding("unchanged")}); err != nil {
		t.Fatal(err)
	}
	unchanged := p.live["b"].registration
	if err := p.refresh(context.Background(), map[string]advertisedBinding{"a": testBinding("new"), "b": testBinding("unchanged")}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sequence, ",") != "start:old,start:unchanged,stop:old,start:new" || p.live["b"].registration != unchanged {
		t.Fatal(sequence)
	}
	if err := p.refresh(context.Background(), nil); !errors.Is(err, errNoAdvertisement) || len(p.live) != 0 {
		t.Fatal(err)
	}
	p.close() // no repeated Shutdown after withdrawal
}

func TestAdvertisementStartupFailureRollsBackPartialRegistrations(t *testing.T) {
	r := &testRegistration{}
	failure := errors.New("registration failure")
	a, err := newAdvertisement(advertisementService(), func() (map[string]advertisedBinding, error) {
		return map[string]advertisedBinding{"a": testBinding("ok"), "b": testBinding("fail")}, nil
	}, func(_ ClientService, b advertisedBinding) (registration, error) {
		if b.signature == "fail" {
			return nil, failure
		}
		return r, nil
	}, time.Millisecond)
	if a != nil || !errors.Is(err, failure) || r.closed.Load() != 1 {
		t.Fatal("partial startup escaped", a, err, r.closed.Load())
	}
}

func TestAdvertisementRefreshRetriesReportsRecoversAndJoins(t *testing.T) {
	var phase, active atomic.Int32
	logs := recording.NewLogger()
	logs.SetLevel(level.Debug)
	s := advertisementService()
	s.Logger = logs
	failure := errors.New("SP:SECRET endpoint details must not be logged")
	snapshot := func() (map[string]advertisedBinding, error) {
		switch phase.Load() {
		case 3:
			return nil, nil
		case 4:
			return nil, failure
		case 0:
			return map[string]advertisedBinding{"a": testBinding("old")}, nil
		default:
			return map[string]advertisedBinding{"a": testBinding("new")}, nil
		}
	}
	register := func(_ ClientService, b advertisedBinding) (registration, error) {
		if phase.Load() == 1 {
			return nil, failure
		}
		active.Add(1)
		return &testRegistration{stop: func() { active.Add(-1) }}, nil
	}
	a, err := newAdvertisement(s, snapshot, register, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if predicate() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("publication health did not settle", a.Err(), active.Load())
	}
	phase.Store(1)
	select {
	case e := <-a.Errors():
		if !errors.Is(e, failure) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing refresh failure")
	}
	wait(func() bool { return active.Load() == 0 && a.Err() != nil })
	phase.Store(2)
	wait(func() bool { return active.Load() == 1 && a.Err() == nil })
	phase.Store(3)
	wait(func() bool { return active.Load() == 0 && errors.Is(a.Err(), errNoAdvertisement) })
	phase.Store(4)
	wait(func() bool { return errors.Is(a.Err(), failure) })
	phase.Store(2)
	wait(func() bool { return active.Load() == 1 && a.Err() == nil })
	var group sync.WaitGroup
	for range 8 {
		group.Go(a.Close)
	}
	group.Wait()
	if active.Load() != 0 {
		t.Fatal("Close returned before registrations stopped")
	}
	for range a.Errors() {
	} // buffered notifications are finite; channel is closed
	for _, event := range logs.GetAll() {
		if err := event.ForEach(func(_ string, v any) error {
			if value, ok := v.(string); ok && (strings.Contains(value, "SP:SECRET") || strings.Contains(value, "private-name")) {
				t.Fatal("private refresh detail logged")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdvertisementCloseDuringRegistrationCannotPublishAfterCancellation(t *testing.T) {
	var changed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	old, next := &testRegistration{}, &testRegistration{}
	snapshot := func() (map[string]advertisedBinding, error) {
		signature := "old"
		if changed.Load() {
			signature = "new"
		}
		return map[string]advertisedBinding{"a": testBinding(signature)}, nil
	}
	a, err := newAdvertisement(advertisementService(), snapshot, func(_ ClientService, b advertisedBinding) (registration, error) {
		if b.signature == "old" {
			return old, nil
		}
		close(entered)
		<-release
		return next, nil
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	select {
	case <-entered:
	case <-time.After(time.Second):
		a.Close()
		t.Fatal("replacement did not enter registrar")
	}
	done := make(chan struct{})
	go func() { a.Close(); close(done) }()
	// Close has to cancel the watcher before the blocked registrar returns.
	// Its own context is private, so observe cancellation via the handle.
	a.cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not join blocked registration")
	}
	if old.closed.Load() != 1 || next.closed.Load() != 1 {
		t.Fatal("a late registration escaped cancellation")
	}
	var zero Advertisement
	zero.Close()
	zero.Close()
	var nilAd *Advertisement
	nilAd.Close()
}
