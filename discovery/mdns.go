// Package discovery implements the two mutually exclusive Sendspin mDNS modes.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/libp2p/zeroconf/v2"
)

type Endpoint struct{ Name, URL string }
type Advertisement struct {
	servers []*zeroconf.Server
	once    sync.Once
}

func (a *Advertisement) Close() {
	if a != nil {
		a.once.Do(func() {
			for _, server := range a.servers {
				server.Shutdown()
			}
		})
	}
}

// ClientService announces one client. Instance should be a stable, unique DNS-SD
// label (the SDK uses ClientID), independently of the optional friendly Name.
// Addresses, when supplied, restrict publication to matching interface addresses.
type ClientService struct {
	Instance, Name string
	Port           int
	Path           string
	Interfaces     []net.Interface
	Addresses      []net.IP
}

// Advertise announces a listening client, never a Sendspin server.
func Advertise(name string, port int, path string, interfaces []net.Interface) (*Advertisement, error) {
	return AdvertiseClient(ClientService{Instance: name, Name: name, Port: port, Path: path, Interfaces: interfaces})
}

// AdvertiseClient publishes only reachable interface-specific addresses. Close
// is synchronous, safe for concurrent calls, and unregisters only this instance.
func AdvertiseClient(service ClientService) (*Advertisement, error) {
	text, err := serviceText(service)
	if err != nil {
		return nil, err
	}
	interfaces, err := multicastInterfaces(service.Interfaces)
	if err != nil {
		return nil, err
	}
	advertisement := &Advertisement{}
	for _, iface := range interfaces {
		ips, err := InterfaceIPs(iface)
		if err != nil {
			advertisement.Close()
			return nil, err
		}
		var addresses []string
		for _, ip := range ips {
			if len(service.Addresses) == 0 || slices.ContainsFunc(service.Addresses, func(wanted net.IP) bool { return wanted.Equal(ip) }) {
				addresses = append(addresses, ip.String())
			}
		}
		if len(addresses) == 0 {
			continue
		}
		id := sha256.Sum256([]byte(service.Instance + "\x00" + iface.Name))
		host := "sendspin-" + hex.EncodeToString(id[:8]) + ".local."
		// Numeric escapes also survive the dependency's trailing-dot trimming.
		instance := strings.NewReplacer("\\", "\\092", ".", "\\046").Replace(service.Instance)
		server, err := zeroconf.RegisterProxy(instance, "_sendspin._tcp", "local.", service.Port, host, addresses, text, []net.Interface{iface})
		if err != nil {
			advertisement.Close()
			return nil, err
		}
		advertisement.servers = append(advertisement.servers, server)
	}
	if len(advertisement.servers) == 0 {
		return nil, errors.New("sendspin: no multicast interface matches the advertised addresses")
	}
	return advertisement, nil
}

func serviceText(service ClientService) ([]string, error) {
	if service.Instance == "" || len(service.Instance) > 63 || !utf8.ValidString(service.Instance) || strings.ContainsAny(service.Instance, "\x00\r\n") || service.Port < 1 || service.Port > 65535 || !validPath(service.Path) || len("path="+service.Path) > 255 {
		return nil, errors.New("sendspin: invalid mDNS advertisement")
	}
	text := []string{"path=" + service.Path}
	// DNS TXT strings have a 255-byte limit. The friendly name is optional,
	// unlike path; long wire-protocol names must not break discovery.
	if service.Name != "" && len("name="+service.Name) <= 255 && utf8.ValidString(service.Name) {
		text = append(text, "name="+service.Name)
	}
	return text, nil
}

func multicastInterfaces(selected []net.Interface) ([]net.Interface, error) {
	if len(selected) == 0 {
		var err error
		selected, err = net.Interfaces()
		if err != nil {
			return nil, err
		}
	}
	var result []net.Interface
	for _, iface := range selected {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagMulticast != 0 {
			result = append(result, iface)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("sendspin: no active multicast interface")
	}
	return result, nil
}

// InterfaceIPs returns usable unicast addresses configured on an interface.
func InterfaceIPs(iface net.Interface) ([]net.IP, error) {
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var result []net.IP
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			continue
		}
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
			continue
		}
		result = append(result, ip)
	}
	return result, nil
}

// Browse discovers servers until ctx is canceled. emit must return promptly.
// Duplicate advertisements can be emitted; selection/reconnect is the caller's
// policy. Interface/address changes are checked once per second. An explicit
// interface selection never broadens to other interfaces. Link-local IPv6 scopes
// come from the receiving interface, never from peer TXT data.
func Browse(ctx context.Context, interfaces []net.Interface, emit func(Endpoint)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if emit == nil {
		return errors.New("sendspin: nil discovery callback")
	}
	selected := slices.Clone(interfaces)
	snapshot := func() (networkSnapshot, error) {
		all, err := net.Interfaces()
		if err != nil {
			return networkSnapshot{}, err
		}
		return snapshotInterfaces(all, selected, InterfaceIPs)
	}
	lookup := func(ctx context.Context, family zeroconf.IPType, ifaces []net.Interface, entries chan<- *zeroconf.ServiceEntry) error {
		ips, err := InterfaceIPs(ifaces[0])
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(ips, func(ip net.IP) bool { return (ip.To4() != nil) == (family == zeroconf.IPv4) }) {
			return errors.New("sendspin: IP family has no configured multicast address")
		}
		return zeroconf.Browse(ctx, "_sendspin-server._tcp", "local.", entries, zeroconf.SelectIfaces(ifaces), zeroconf.SelectIPTraffic(family))
	}
	return watchInterfaces(ctx, snapshot, lookup, emit, time.Second)
}

type lookupFunc func(context.Context, zeroconf.IPType, []net.Interface, chan<- *zeroconf.ServiceEntry) error

func browseServices(parent context.Context, interfaces []net.Interface, emit func(Endpoint), lookup lookupFunc) error {
	zone := ""
	if len(interfaces) == 1 && interfaces[0].Index > 0 {
		zone = strconv.Itoa(interfaces[0].Index)
	}
	ctx, cancel := context.WithCancel(parent)
	entries4, entries6 := make(chan *zeroconf.ServiceEntry), make(chan *zeroconf.ServiceEntry)
	done := make(chan error, 2)
	remaining := 2
	// Separate families avoid losing working IPv4 discovery if IPv6 setup is
	// unavailable (and avoid a partially initialized dual-stack resolver).
	for _, worker := range []struct {
		family  zeroconf.IPType
		entries chan *zeroconf.ServiceEntry
	}{{zeroconf.IPv4, entries4}, {zeroconf.IPv6, entries6}} {
		go func() { done <- lookup(ctx, worker.family, slices.Clone(interfaces), worker.entries) }()
	}
	// The upstream subscriber send is blocking. Keep draining on cancellation,
	// including panic unwinding, so its socket-owning goroutine can shut down.
	defer func() {
		cancel()
		for remaining > 0 {
			select {
			case <-done: // Cleanup: the return path/panic is primary; drain results.
				remaining--
			case _, ok := <-entries4:
				if !ok {
					entries4 = nil
				}
			case _, ok := <-entries6:
				if !ok {
					entries6 = nil
				}
			}
		}
	}()
	ctxDone := ctx.Done()
	var failures []error
	stopping := false
	for remaining > 0 {
		select {
		case <-ctxDone:
			stopping = true
			cancel()
			ctxDone = nil
		case err := <-done:
			remaining--
			if err != nil {
				failures = append(failures, err)
			}
		case entry, ok := <-entries4:
			if !ok {
				entries4 = nil
				continue
			}
			if !stopping && ctx.Err() == nil {
				for _, endpoint := range scopedEndpoints(entry, zone) {
					emit(endpoint)
				}
			}
		case entry, ok := <-entries6:
			if !ok {
				entries6 = nil
				continue
			}
			if !stopping && ctx.Err() == nil {
				for _, endpoint := range scopedEndpoints(entry, zone) {
					emit(endpoint)
				}
			}
		}
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if len(failures) > 0 {
		return fmt.Errorf("sendspin: mDNS browse failed: %w", errors.Join(failures...))
	}
	return nil
}
func validPath(path string) bool {
	u, err := url.ParseRequestURI(path)
	return err == nil && strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.ContainsAny(path, "#\r\n") && u.Host == "" && u.Fragment == ""
}
func endpoints(entry *zeroconf.ServiceEntry) []Endpoint {
	return scopedEndpoints(entry, "")
}

func scopedEndpoints(entry *zeroconf.ServiceEntry, zone string) []Endpoint {
	if entry == nil {
		return nil
	}
	path, name := "", entry.Instance
	seen := make(map[string]bool)
	for _, txt := range entry.Text {
		key, value, ok := strings.Cut(txt, "=")
		key = strings.ToLower(key)
		if seen[key] {
			continue
		}
		seen[key] = true
		if ok {
			if key == "path" {
				path = value
			}
			if key == "name" && value != "" {
				name = value
			}
		}
	}
	if !validPath(path) || entry.Port < 1 || entry.Port > 65535 {
		return nil
	}
	var result []Endpoint
	request, _ := url.ParseRequestURI(path)
	for _, ip := range append(append([]net.IP(nil), entry.AddrIPv4...), entry.AddrIPv6...) {
		if ip.To16() == nil || ip.IsUnspecified() || ip.IsMulticast() || (ip.To4() == nil && ip.IsLinkLocalUnicast() && zone == "") || ip.Equal(net.IPv4bcast) {
			continue
		}
		endpoint := *request
		endpoint.Scheme = "ws"
		host := ip.String()
		if ip.To4() == nil && ip.IsLinkLocalUnicast() {
			host += "%" + zone
		}
		endpoint.Host = net.JoinHostPort(host, strconv.Itoa(entry.Port))
		result = append(result, Endpoint{Name: name, URL: endpoint.String()})
	}
	return result
}
