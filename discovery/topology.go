package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

type networkSnapshot struct {
	interfaces []net.Interface
	signature  string
}

type browseResult struct{ err error }

func snapshotInterfaces(all, selected []net.Interface, addresses func(net.Interface) ([]net.IP, error)) (networkSnapshot, error) {
	var result networkSnapshot
	var keys []string
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		if len(selected) > 0 && !slices.ContainsFunc(selected, func(s net.Interface) bool { return s.Index == iface.Index && s.Name == iface.Name }) {
			continue
		}
		ips, err := addresses(iface)
		if err != nil {
			return result, err
		}
		if len(ips) == 0 {
			continue
		}
		var names []string
		for _, ip := range ips {
			names = append(names, ip.String())
		}
		slices.Sort(names)
		keys = append(keys, fmt.Sprintf("%d/%s/%d/%d/%s/%s", iface.Index, iface.Name, iface.Flags, iface.MTU, iface.HardwareAddr.String(), strings.Join(names, ",")))
		result.interfaces = append(result.interfaces, iface)
	}
	slices.Sort(keys)
	result.signature = strings.Join(keys, "\n")
	return result, nil
}

// Each resolver owns exactly one interface, so receiving scope cannot be lost
// in a multi-interface dependency result. The outer loop serializes callbacks.
func browseInterfaces(parent context.Context, interfaces []net.Interface, emit func(Endpoint), lookup lookupFunc) error {
	ctx, cancel := context.WithCancel(parent)
	entries := make(chan Endpoint)
	done := make(chan browseResult, len(interfaces))
	remaining := len(interfaces)
	for _, iface := range interfaces {
		go func() {
			done <- browseResult{browseServices(ctx, []net.Interface{iface}, func(e Endpoint) {
				select {
				case entries <- e:
				case <-ctx.Done():
				}
			}, lookup)}
		}()
	}
	defer func() {
		cancel()
		for remaining > 0 {
			<-done // cleanup preserves the primary return/panic
			remaining--
		}
	}()
	var failures []error
	for remaining > 0 {
		select {
		case <-parent.Done():
			return parent.Err()
		case result := <-done:
			remaining--
			if result.err != nil {
				failures = append(failures, result.err)
			}
		case e := <-entries:
			if parent.Err() == nil {
				emit(e)
			}
		}
	}
	return errors.Join(failures...)
}

func watchInterfaces(parent context.Context, snapshot func() (networkSnapshot, error), lookup lookupFunc, emit func(Endpoint), interval time.Duration) error {
	current, err := snapshot()
	if err != nil {
		return err
	}
	if len(current.interfaces) == 0 {
		return errors.New("sendspin: no active multicast interface with usable addresses")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithCancel(parent)
		entries := make(chan Endpoint)
		done := make(chan browseResult, 1)
		if len(current.interfaces) > 0 {
			epochInterfaces := slices.Clone(current.interfaces)
			finished := done
			go func() {
				finished <- browseResult{browseInterfaces(ctx, epochInterfaces, func(e Endpoint) {
					select {
					case entries <- e:
					case <-ctx.Done():
					}
				}, lookup)}
			}()
		} else {
			done = nil
		}
		// Drain and join the entire old generation before starting a new one.
		join := func() {
			cancel()
			if done != nil {
				<-done // cancellation cleanup is not the primary result
			}
		}
		changed := false
		func() {
			defer join() // also protects callback panic unwinding
			for !changed && err == nil {
				select {
				case <-parent.Done():
					err = parent.Err()
				case e := <-entries:
					if parent.Err() == nil {
						emit(e)
					}
				case result := <-done:
					done = nil
					if parent.Err() != nil {
						err = parent.Err()
					} else {
						// A last interface can disappear before the polling tick.
						// Reconcile that topology loss instead of mistaking it for
						// a permanent resolver/setup error on unchanged interfaces.
						var next networkSnapshot
						next, err = snapshot()
						if err == nil && next.signature != current.signature {
							current = next
							changed = true
						} else if err == nil && result.err != nil {
							err = result.err
						} else if err == nil {
							err = errors.New("sendspin: discovery stopped unexpectedly")
						}
					}
				case <-ticker.C:
					var next networkSnapshot
					next, err = snapshot()
					if err == nil && next.signature != current.signature {
						current = next
						changed = true
					}
				}
			}
		}()
		if err != nil {
			return err
		}
	}
}
