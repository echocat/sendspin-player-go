// Package discovery implements the two mutually exclusive Sendspin mDNS modes.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	zeroconf "github.com/echocat/sendspin-player-go/internal/mdns"
	log "github.com/echocat/slf4g"
)

type Endpoint struct{ Name, URL string }

// ClientService announces one client. Instance should be a stable, unique DNS-SD
// label (the SDK uses ClientID), independently of the optional friendly Name.
// Addresses, when supplied, restrict publication to matching interface addresses.
type ClientService struct {
	Instance, Name string
	Port           int
	Path           string
	Interfaces     []net.Interface
	Addresses      []net.IP
	// IPVersion restricts published addresses: 0 means both, 4 IPv4, 6 IPv6.
	// The restriction also applies to interfaces/addresses appearing later.
	IPVersion int
	// Logger optionally selects an application-owned slf4g logger for refresh
	// health. Nil uses the discovery package facade. Sinks must return promptly.
	Logger log.Logger
}

// Advertise announces a listening client, never a Sendspin server.
func Advertise(name string, port int, path string, interfaces []net.Interface) (*Advertisement, error) {
	return AdvertiseClient(ClientService{Instance: name, Name: name, Port: port, Path: path, Interfaces: interfaces})
}

// AdvertiseClient publishes interface-specific addresses and refreshes them once
// per second. Explicit Interfaces/Addresses and IPVersion remain restrictions,
// not startup hints. Errors/Err expose later failures while refresh retries.
// Close synchronously joins the watcher and unregisters only this instance.
func AdvertiseClient(service ClientService) (*Advertisement, error) {
	service = copyService(service)
	snapshot := func() (map[string]advertisedBinding, error) {
		interfaces, err := net.Interfaces()
		if err != nil {
			return nil, err
		}
		return advertisementBindings(service, interfaces, InterfaceIPs)
	}
	return newAdvertisement(service, snapshot, registerBinding, time.Second)
}

func serviceText(service ClientService) ([]string, error) {
	if service.IPVersion != 0 && service.IPVersion != 4 && service.IPVersion != 6 {
		return nil, errors.New("sendspin: invalid advertised IP version")
	}
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
