package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	zeroconf "github.com/echocat/sendspin-player-go/internal/mdns"
	log "github.com/echocat/slf4g"
)

var advertisementLogger = log.GetLoggerForCurrentPackage()
var errNoAdvertisement = errors.New("sendspin: no multicast interface matches the advertised addresses")

// Advertisement owns its registrations and topology watcher. The zero value
// can be closed safely but does not represent a running advertisement.
type Advertisement struct {
	cancel context.CancelFunc
	done   chan struct{}
	errors chan error
	once   sync.Once
	mu     sync.RWMutex
	err    error
	logger log.Logger
}

// Close withdraws reachable records, closes sockets and joins the watcher.
// Remote caches on a disconnected link can only expire by their DNS TTL.
func (a *Advertisement) Close() {
	if a != nil {
		a.once.Do(func() {
			if a.cancel != nil {
				a.cancel()
			}
			if a.done != nil {
				<-a.done
			}
		})
	}
}

// Errors reports refresh failures without blocking the publisher. Notifications
// are best effort (one buffered error); the channel closes when Close completes.
// Err is the current health snapshot, including recovery after a notification.
func (a *Advertisement) Errors() <-chan error {
	if a == nil {
		return nil
	}
	return a.errors
}

// Err returns the latest refresh failure, or nil after successful publication.
// Nil is not a promise that a remote peer received an announcement.
func (a *Advertisement) Err() error {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.err
}

func (a *Advertisement) report(err error) {
	a.mu.Lock()
	previous := a.err
	a.err = err
	a.mu.Unlock()
	if err != nil {
		if previous != nil && previous.Error() == err.Error() {
			return
		}
		select {
		case a.errors <- err:
		default:
		}
		// Do not log raw errors, interface names, addresses or service fields.
		a.logger.Warn("mDNS advertisement refresh failed; retrying")
	} else if previous != nil {
		a.logger.Debug("mDNS advertisement publication recovered")
	}
}

type advertisedBinding struct {
	iface     net.Interface
	addresses []string
	signature string
}
type registration interface{ Shutdown() }
type registerFunc func(ClientService, advertisedBinding) (registration, error)
type liveBinding struct {
	signature    string
	registration registration
}
type publication struct {
	service  ClientService
	register registerFunc
	live     map[string]liveBinding
}

func copyService(s ClientService) ClientService {
	s.Interfaces = slices.Clone(s.Interfaces)
	for i := range s.Interfaces {
		s.Interfaces[i].HardwareAddr = slices.Clone(s.Interfaces[i].HardwareAddr)
	}
	s.Addresses = slices.Clone(s.Addresses)
	for i := range s.Addresses {
		s.Addresses[i] = slices.Clone(s.Addresses[i])
	}
	return s
}

func advertisementBindings(s ClientService, interfaces []net.Interface, addresses func(net.Interface) ([]net.IP, error)) (map[string]advertisedBinding, error) {
	result := map[string]advertisedBinding{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		if len(s.Interfaces) > 0 && !slices.ContainsFunc(s.Interfaces, func(wanted net.Interface) bool { return wanted.Index == iface.Index && wanted.Name == iface.Name }) {
			continue
		}
		ips, err := addresses(iface)
		if err != nil {
			return nil, err
		}
		var selected []string
		for _, ip := range ips {
			if ip.To16() == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() || ip.Equal(net.IPv4bcast) {
				continue
			}
			if (s.IPVersion == 4 && ip.To4() == nil) || (s.IPVersion == 6 && ip.To4() != nil) {
				continue
			}
			if len(s.Addresses) > 0 && !slices.ContainsFunc(s.Addresses, func(wanted net.IP) bool { return wanted.Equal(ip) }) {
				continue
			}
			selected = append(selected, ip.String())
		}
		slices.Sort(selected)
		selected = slices.Compact(selected)
		if len(selected) == 0 {
			continue
		}
		key := fmt.Sprintf("%d/%s", iface.Index, iface.Name)
		signature := fmt.Sprintf("%d/%d/%s/%s", iface.Flags, iface.MTU, iface.HardwareAddr.String(), strings.Join(selected, ","))
		result[key] = advertisedBinding{iface, selected, signature}
	}
	return result, nil
}

func registerBinding(s ClientService, b advertisedBinding) (registration, error) {
	text, err := serviceText(s)
	if err != nil {
		return nil, err
	}
	id := sha256.Sum256([]byte(s.Instance + "\x00" + b.iface.Name))
	host := "sendspin-" + hex.EncodeToString(id[:8]) + ".local."
	instance := strings.NewReplacer("\\", "\\092", ".", "\\046").Replace(s.Instance)
	return zeroconf.RegisterProxy(instance, "_sendspin._tcp", "local.", s.Port, host, b.addresses, text, []net.Interface{b.iface})
}

func (p *publication) close() {
	for key, value := range p.live {
		value.registration.Shutdown()
		delete(p.live, key)
	}
}
func (p *publication) refresh(ctx context.Context, wanted map[string]advertisedBinding) error {
	// Withdraw obsolete bindings before exposing replacement addresses. Leave
	// unchanged interfaces alone so unrelated peers do not see false goodbyes.
	for key, value := range p.live {
		next, ok := wanted[key]
		if !ok || next.signature != value.signature {
			value.registration.Shutdown()
			delete(p.live, key)
		}
	}
	keys := make([]string, 0, len(wanted))
	for key := range wanted {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var failures []error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := p.live[key]; ok {
			continue
		}
		r, err := p.register(p.service, wanted[key])
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if r == nil {
			failures = append(failures, errors.New("sendspin: empty mDNS registration"))
			continue
		}
		if err := ctx.Err(); err != nil {
			r.Shutdown()
			return err
		}
		p.live[key] = liveBinding{wanted[key].signature, r}
	}
	if len(wanted) == 0 {
		failures = append(failures, errNoAdvertisement)
	}
	return errors.Join(failures...)
}

func newAdvertisement(s ClientService, snapshot func() (map[string]advertisedBinding, error), register registerFunc, interval time.Duration) (*Advertisement, error) {
	if _, err := serviceText(s); err != nil {
		return nil, err
	}
	wanted, err := snapshot()
	if err != nil {
		return nil, err
	}
	p := &publication{service: s, register: register, live: map[string]liveBinding{}}
	if err = p.refresh(context.Background(), wanted); err != nil {
		p.close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	logger := s.Logger
	if logger == nil {
		logger = advertisementLogger
	}
	a := &Advertisement{cancel: cancel, done: make(chan struct{}), errors: make(chan error, 1), logger: logger}
	go func() {
		defer close(a.done)
		defer close(a.errors)
		defer p.close()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				wanted, err := snapshot()
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					p.close()
					a.report(err)
					continue
				}
				err = p.refresh(ctx, wanted)
				if ctx.Err() != nil {
					return
				}
				a.report(err)
			}
		}
	}()
	return a, nil
}
