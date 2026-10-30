//go:build e2e

package mdnstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/discovery"
)

func isolatedMDNS(t *testing.T) (net.Interface, net.IP) {
	t.Helper()
	if fixtureUnavailable != nil {
		t.Skip(fixtureUnavailable)
	}
	if runtime.GOOS != "linux" || os.Getenv("SENDSPIN_MDNS_TEST_NETWORK") != "internal" {
		t.Fatal("real multicast tests require the private container started by TestMain")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		if _, err = os.Stat("/run/.containerenv"); err != nil {
			t.Fatal("refusing multicast tests outside a container")
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var selected []net.Interface
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagMulticast != 0 && iface.Flags&net.FlagLoopback == 0 {
			selected = append(selected, iface)
		}
	}
	if len(selected) != 1 {
		t.Fatalf("fixture requires one isolated multicast interface, got %v", selected)
	}
	ips, err := discovery.InterfaceIPs(selected[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		if ip.To4() != nil && ip.IsPrivate() {
			return selected[0], ip
		}
	}
	t.Fatal("internal network has no private IPv4 address")
	return net.Interface{}, nil
}

func mdnsClient(t *testing.T, cred sendspin.Credentials, ref referenceServer, name string) *sendspin.Client {
	t.Helper()
	cred.Records = []sendspin.PairingRecord{{ServerID: ref.id, PSK: sendspin.Secret{111}}}
	store := &sendspin.MemoryStore{}
	if err := store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	client, err := sendspin.New(context.Background(), sendspin.Config{Name: name, Store: store, Roles: []sendspin.Role{sendspin.Controller}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func serveMDNSClient(t *testing.T, client *sendspin.Client, ip net.IP) (int, func()) {
	return serveMDNSAddress(t, client, "tcp4", &net.TCPAddr{IP: ip})
}

func serveMDNSAddress(t *testing.T, client *sendspin.Client, network string, address *net.TCPAddr) (int, func()) {
	t.Helper()
	listener, err := net.ListenTCP(network, address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Serve(ctx, listener) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("mDNS Serve exit: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("mDNS Serve did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return listener.Addr().(*net.TCPAddr).Port, stop
}

func ipv6MDNS(t *testing.T) (net.Interface, net.IP) {
	t.Helper()
	iface, _ := isolatedMDNS(t)
	ips, err := discovery.InterfaceIPs(iface)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		if ip.To4() == nil && ip.IsLinkLocalUnicast() {
			return iface, ip
		}
	}
	t.Fatal("isolated IPv6 fixture has no link-local address")
	return net.Interface{}, nil
}

func TestE2EMDNSIPv6ScopedDiscoveryAndEncryptedSession(t *testing.T) {
	iface, ip := ipv6MDNS(t)
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startMDNSReference(t, cred, iface, ip, "advertise_server", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	found := make(chan discovery.Endpoint, 1)
	browseDone := make(chan error, 1)
	go func() {
		browseDone <- discovery.Browse(ctx, []net.Interface{iface}, func(e discovery.Endpoint) {
			select {
			case found <- e:
			default:
			}
		})
	}()
	var endpoint discovery.Endpoint
	select {
	case endpoint = <-found:
	case <-ctx.Done():
		t.Fatal("IPv6 multicast discovery timed out")
	}
	cancel()
	if err = <-browseDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !strings.Contains(endpoint.URL, "%25"+strconv.Itoa(iface.Index)+"]") {
		t.Fatal("link-local scope missing", endpoint.URL)
	}
	client := mdnsClient(t, cred, ref, "Scoped IPv6 client")
	sessionCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Connect(sessionCtx, endpoint.URL) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("scoped IPv6 session leaked")
		}
	})
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id && client.Snapshot().Available })
}

func TestE2EMDNSIPv6ServerInitiatedDiscovery(t *testing.T) {
	iface, ip := ipv6MDNS(t)
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startMDNSReference(t, cred, iface, ip, "discover_clients", nil)
	client := mdnsClient(t, cred, ref, "Scoped IPv6 player")
	_, stop := serveMDNSAddress(t, client, "tcp6", &net.TCPAddr{IP: ip, Zone: strconv.Itoa(iface.Index)})
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id && client.Snapshot().Available })
	stop()
	eventually(t, func() bool {
		clients := ref.peer.observed(t)
		_, present := clients[client.ClientID()]
		return !present
	})
}

func TestE2EMDNSClientInitiatedDiscoveryAndModeReservation(t *testing.T) {
	iface, ip := isolatedMDNS(t)
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startMDNSReference(t, cred, iface, ip, "advertise_server", nil)
	client := mdnsClient(t, cred, ref, "Client-initiated mDNS")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.DiscoverAndRun(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("DiscoverAndRun exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("discovery session did not stop")
		}
	})
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id && client.Snapshot().Available })
	if client.Snapshot().ServerName != "RC1 independent E2E" {
		t.Fatal("mDNS name hint replaced authoritative server/hello name")
	}
	if err = client.Connect(ctx, "ws://127.0.0.1:1/sendspin"); !errors.Is(err, sendspin.ErrConnectionMode) {
		t.Fatal("discovered connection lost its outbound reservation", err)
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err = client.Serve(ctx, listener); !errors.Is(err, sendspin.ErrConnectionMode) {
		t.Fatal("inbound advertising was admitted during outbound mode", err)
	}
	if len(ref.peer.observed(t)) != 0 {
		t.Fatal("outbound player advertised the client service simultaneously")
	}
}

func TestE2EMDNSServerInitiatedUniqueInstancesAndGoodbye(t *testing.T) {
	iface, ip := isolatedMDNS(t)
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startMDNSReference(t, cred, iface, ip, "discover_clients", nil)
	const name = "Kitchen.Speaker Ä"
	client := mdnsClient(t, cred, ref, name)
	port, stop := serveMDNSClient(t, client, ip)
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id && client.Snapshot().Available })
	var record map[string]any
	eventually(t, func() bool {
		clients := ref.peer.observed(t)
		raw, ok := clients[client.ClientID()]
		if ok {
			record = raw.(map[string]any)
		}
		return ok
	})
	properties := record["properties"].(map[string]any)
	if properties["path"] != "/sendspin" || properties["name"] != name || int(record["port"].(float64)) != port {
		t.Fatal("incorrect independent discovery metadata", record)
	}
	addresses := record["addresses"].([]any)
	if len(addresses) != 1 || addresses[0] != ip.String() {
		t.Fatal("advertisement exposed addresses outside the TCP binding", addresses)
	}
	second, err := sendspin.New(context.Background(), sendspin.Config{Name: name, Store: &sendspin.MemoryStore{}, Roles: []sendspin.Role{sendspin.Controller}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = serveMDNSClient(t, second, ip)
	eventually(t, func() bool {
		clients := ref.peer.observed(t)
		_, one := clients[client.ClientID()]
		_, two := clients[second.ClientID()]
		return one && two
	})
	stop()
	eventually(t, func() bool {
		clients := ref.peer.observed(t)
		_, one := clients[client.ClientID()]
		_, two := clients[second.ClientID()]
		return !one && two
	})
}

func TestE2EMDNSLongFriendlyNameRemainsUsable(t *testing.T) {
	iface, ip := isolatedMDNS(t)
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startMDNSReference(t, cred, iface, ip, "discover_clients", nil)
	name := strings.Repeat("Küche ", 60)
	client := mdnsClient(t, cred, ref, name)
	_, _ = serveMDNSClient(t, client, ip)
	eventually(t, func() bool { return client.Snapshot().Available })
	status := ref.control(t, map[string]any{"command": "status"})
	record := ref.peer.observed(t)[client.ClientID()].(map[string]any)
	if _, present := record["properties"].(map[string]any)["name"]; present {
		t.Fatal("overlong friendly name was encoded as a TXT string")
	}
	if status["client_name"] != name {
		t.Fatal("omitted TXT hint changed client/hello name")
	}
}

func TestE2EMDNSMissingPathIsIgnoredAndCancellationReleasesMode(t *testing.T) {
	iface, ip := isolatedMDNS(t)
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startMDNSReference(t, cred, iface, ip, "advertise_server", map[string]string{"name": "No path"})
	client := mdnsClient(t, cred, ref, "Missing-path test")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err = client.DiscoverAndRun(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("invalid discovery was not ignored/cancelable", err)
	}
	if client.Snapshot().ServerID != "" {
		t.Fatal("missing path defaulted to a WebSocket endpoint")
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	select {
	case <-connection.done:
		t.Fatal("canceled lookup retained outbound mode or sockets", connection.err)
	default:
	}
}

func ownMDNSSockets(t *testing.T) int {
	t.Helper()
	inodes := map[string]bool{}
	for _, name := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 9 && strings.HasSuffix(fields[1], ":14E9") {
				inodes[fields[9]] = true
			}
		}
	}
	files, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, file := range files {
		target, err := os.Readlink("/proc/self/fd/" + file.Name())
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, "socket:[") && inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] {
			count++
		}
	}
	return count
}

func TestE2EMDNSRepeatedCanceledBrowsersReleaseSockets(t *testing.T) {
	iface, _ := isolatedMDNS(t)
	baseline := ownMDNSSockets(t)
	for i := range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := discovery.Browse(ctx, []net.Interface{iface}, func(discovery.Endpoint) {})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("browse cancellation", i, err)
		}
		if remaining := ownMDNSSockets(t); remaining != baseline {
			t.Fatalf("cycle %s leaked mDNS sockets: got %d, baseline %d", strconv.Itoa(i), remaining, baseline)
		}
	}
	fmt.Fprintln(os.Stderr, "mDNS cancellation: all UDP/5353 socket descriptors released")
}

func TestE2EMDNSActualInterfaceAttachAndRemoval(t *testing.T) {
	initial, _ := isolatedMDNS(t)
	if os.Getenv("SENDSPIN_MDNS_TEST_HOTPLUG") != "1" {
		t.Fatal("topology test requires the isolated runner control channel")
	}
	baseline := ownMDNSSockets(t)
	// Both listeners stay open across hotplug. Neither publisher may freeze its
	// startup interfaces or broaden its actual socket address family.
	ad4, err := sendspin.New(context.Background(), sendspin.Config{Name: "Dynamic IPv4", Store: &sendspin.MemoryStore{}, Roles: []sendspin.Role{sendspin.Controller}})
	if err != nil {
		t.Fatal(err)
	}
	port4, stop4 := serveMDNSAddress(t, ad4, "tcp4", &net.TCPAddr{IP: net.IPv4zero})
	ad6, err := sendspin.New(context.Background(), sendspin.Config{Name: "Dynamic IPv6", Store: &sendspin.MemoryStore{}, Roles: []sendspin.Role{sendspin.Controller}})
	if err != nil {
		t.Fatal(err)
	}
	port6, stop6 := serveMDNSAddress(t, ad6, "tcp6", &net.TCPAddr{IP: net.IPv6zero})
	// Serve starts asynchronously. Establish both dual-family publisher socket
	// sets before taking the baseline used for hotplug cleanup assertions.
	eventually(t, func() bool { return ownMDNSSockets(t) == baseline+4 })
	advertisingSockets := ownMDNSSockets(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	found := make(chan discovery.Endpoint, 16)
	go func() {
		done <- discovery.Browse(ctx, nil, func(e discovery.Endpoint) {
			select {
			case found <- e:
			default:
			}
		})
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("topology browser leaked")
			}
		})
	}
	t.Cleanup(stop)
	eventually(t, func() bool { return ownMDNSSockets(t) > advertisingSockets })
	if _, err := fmt.Fprintln(os.Stdout, "[mdns-topology] attach"); err != nil {
		t.Fatal(err)
	}
	detached := false
	detach := func() {
		if !detached {
			if _, err := fmt.Fprintln(os.Stdout, "[mdns-topology] detach"); err != nil {
				t.Error(err)
			}
			detached = true
		}
	}
	t.Cleanup(detach)
	var added net.Interface
	var ip net.IP
	eventually(t, func() bool {
		interfaces, err := net.Interfaces()
		if err != nil {
			return false
		}
		for _, iface := range interfaces {
			if iface.Index == initial.Index || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
				continue
			}
			ips, e := discovery.InterfaceIPs(iface)
			if e != nil {
				continue
			}
			for _, candidate := range ips {
				if candidate.To4() == nil && candidate.IsLinkLocalUnicast() {
					// Engine network-connect can return while IPv6 DAD is still
					// running. Interface.Addrs already lists tentative addresses,
					// but neither our TCP forwarder nor a multicast peer may bind
					// them yet. Probe readiness without touching host networking.
					probe, e := net.ListenTCP("tcp6", &net.TCPAddr{IP: candidate, Zone: strconv.Itoa(iface.Index)})
					if e != nil {
						continue
					}
					if e = probe.Close(); e != nil {
						t.Fatal(e)
					}
					added, ip = iface, candidate
					return true
				}
			}
		}
		return false
	})
	cred, err := sendspin.NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startMDNSReference(t, cred, added, ip, "advertise_server", nil)
	ips, err := discovery.InterfaceIPs(added)
	if err != nil {
		t.Fatal(err)
	}
	var ip4 net.IP
	for _, candidate := range ips {
		if candidate.To4() != nil {
			ip4 = candidate
			break
		}
	}
	if ip4 == nil {
		t.Fatal("second isolated interface has no IPv4 address")
	}
	observer4, stopObserver4 := startDNSPeer(t, added, 4)
	assertAdvertisement := func(snapshot func() map[string]any, client *sendspin.Client, port int, version int) {
		t.Helper()
		var record map[string]any
		expected := 0
		for _, candidate := range ips {
			if (candidate.To4() != nil) == (version == 4) {
				expected++
			}
		}
		t.Cleanup(func() {
			if t.Failed() {
				t.Log("last dynamic advertisement", record, "expected interface", added.Index, added.Name, ips)
			}
		})
		eventually(t, func() bool {
			clients := snapshot()
			raw, ok := clients[client.ClientID()]
			if ok {
				record = raw.(map[string]any)
			}
			if !ok || int(record["port"].(float64)) != port {
				return false
			}
			addresses := record["addresses"].([]any)
			if len(addresses) != expected {
				return false
			}
			for _, raw := range addresses {
				address, _, _ := strings.Cut(raw.(string), "%")
				candidate := net.ParseIP(address)
				found := false
				for _, allowed := range ips {
					found = found || allowed.Equal(candidate)
				}
				if !found || (candidate.To4() != nil) != (version == 4) {
					return false
				}
			}
			return true
		})
		if int(record["port"].(float64)) != port {
			t.Fatal("advertisement changed the existing listener port", record)
		}
		got := record["addresses"].([]any)
		if len(got) != expected {
			t.Fatal("missing/extra interface addresses", got, ips, version)
		}
		for _, raw := range got {
			address, zone, _ := strings.Cut(raw.(string), "%")
			candidate := net.ParseIP(address)
			found := false
			for _, allowed := range ips {
				found = found || allowed.Equal(candidate)
			}
			if !found || (candidate.To4() != nil) != (version == 4) {
				t.Fatal("publisher exposed an unbound address/family", got, version, "expected interface", added.Index, added.Name, ips, "record", record)
			}
			if candidate.IsLinkLocalUnicast() && candidate.To4() == nil {
				// The independent observer owns a different network namespace.
				// A link-local zone must identify its receiving interface, not a
				// publisher-supplied index copied out of TXT data or the SDK.
				if zone != strconv.Itoa(int(record["interface_index"].(float64))) {
					t.Fatal("IPv6 scope did not come from observer's receiving interface", record)
				}
			}
		}
	}
	assertAdvertisement(func() map[string]any {
		return observer4.observed(t)
	}, ad4, port4, 4)
	observerURL := os.Getenv("SENDSPIN_MDNS_OBSERVER_URL")
	if observerURL == "" {
		t.Fatal("isolated observer control address missing")
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	control := &http.Client{Timeout: time.Second, Transport: transport}
	assertAdvertisement(func() map[string]any {
		response, err := control.Get(observerURL + "/status")
		if err != nil {
			return map[string]any{}
		}
		defer func() { _ = response.Body.Close() }()
		var state struct {
			Clients map[string]any `json:"clients"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&state) != nil {
			return map[string]any{}
		}
		return state.Clients
	}, ad6, port6, 6)
	var endpoint discovery.Endpoint
	eventually(t, func() bool {
		for {
			select {
			case e := <-found:
				if strings.Contains(e.URL, "%25"+strconv.Itoa(added.Index)+"]") {
					endpoint = e
					return true
				}
			default:
				return false
			}
		}
	})
	client := mdnsClient(t, cred, ref, "New interface session")
	sessionCtx, endSession := context.WithCancel(context.Background())
	sessionDone := make(chan error, 1)
	go func() { sessionDone <- client.Connect(sessionCtx, endpoint.URL) }()
	var sessionOnce sync.Once
	joinSession := func() {
		sessionOnce.Do(func() {
			endSession()
			select {
			case e := <-sessionDone:
				if !errors.Is(e, context.Canceled) {
					t.Error(e)
				}
			case <-time.After(5 * time.Second):
				t.Error("new-interface session leaked")
			}
		})
	}
	t.Cleanup(joinSession)
	eventually(t, func() bool { return client.Snapshot().Available && client.Snapshot().ServerID == ref.id })
	joinSession()
	// These independent Go peers own sockets in this process, unlike the old
	// Python observers. Stop them before comparing the SDK's descriptor budget.
	ref.stop()
	stopObserver4()
	detach()
	eventually(t, func() bool {
		interfaces, e := net.Interfaces()
		if e != nil {
			return false
		}
		for _, iface := range interfaces {
			if iface.Index == added.Index {
				return false
			}
		}
		return true
	})
	// The removed generation's per-interface UDP sockets must disappear while
	// discovery remains live on the original interface.
	eventually(t, func() bool { return ownMDNSSockets(t) == advertisingSockets+2 })
	stop()
	stop4()
	stop6()
	if ownMDNSSockets(t) != baseline {
		t.Fatal("topology changes leaked resolver sockets")
	}
}
