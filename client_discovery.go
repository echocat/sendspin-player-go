package sendspin

import (
	"errors"
	"net"
	"strconv"
	"syscall"

	"github.com/echocat/sendspin-player-go/discovery"
)

func (c *Client) serviceForListener(listener net.Listener) (discovery.ClientService, error) {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil {
		return discovery.ClientService{}, errors.New("sendspin: mDNS requires a TCP listener with a concrete address family")
	}
	ipv4, ipv6 := address.IP.To4() != nil, address.IP.To4() == nil
	if address.IP.IsUnspecified() && ipv6 {
		provider, ok := listener.(interface {
			SyscallConn() (syscall.RawConn, error)
		})
		if !ok {
			return discovery.ClientService{}, errors.New("sendspin: IPv6 wildcard discovery requires listener SyscallConn")
		}
		raw, err := provider.SyscallConn()
		if err != nil {
			return discovery.ClientService{}, err
		}
		var only bool
		var optionErr error
		if err = raw.Control(func(fd uintptr) { only, optionErr = socketIPv6Only(fd) }); err != nil {
			return discovery.ClientService{}, err
		}
		if optionErr != nil {
			return discovery.ClientService{}, optionErr
		}
		ipv4 = !only
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return discovery.ClientService{}, err
	}
	service, err := selectListenerAddresses(address, ipv4, ipv6, interfaces, discovery.InterfaceIPs)
	if err != nil {
		return service, err
	}
	service.Instance, service.Name = c.ClientID(), c.cfg.Name
	service.Port, service.Path = address.Port, DefaultWebSocketPath
	return service, nil
}

func selectListenerAddresses(address *net.TCPAddr, ipv4, ipv6 bool, interfaces []net.Interface, addresses func(net.Interface) ([]net.IP, error)) (discovery.ClientService, error) {
	var service discovery.ClientService
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		if address.Zone != "" && address.Zone != iface.Name && address.Zone != strconv.Itoa(iface.Index) {
			continue
		}
		ips, err := addresses(iface)
		if err != nil {
			return service, err
		}
		matched := false
		for _, ip := range ips {
			if (ip.To4() != nil && !ipv4) || (ip.To4() == nil && !ipv6) {
				continue
			}
			if !address.IP.IsUnspecified() && !address.IP.Equal(ip) {
				continue
			}
			service.Addresses = append(service.Addresses, append(net.IP(nil), ip...))
			matched = true
		}
		if matched {
			service.Interfaces = append(service.Interfaces, iface)
		}
	}
	if len(service.Addresses) == 0 {
		return service, errors.New("sendspin: listener has no reachable multicast interface; disable discovery for loopback-only listeners")
	}
	return service, nil
}
