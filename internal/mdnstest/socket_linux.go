//go:build linux

package mdnstest

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func multicastSocketControl(network, _ string, raw syscall.RawConn) error {
	var configureErr error
	err := raw.Control(func(fd uintptr) {
		for _, option := range []int{unix.SO_REUSEADDR, unix.SO_REUSEPORT} {
			if configureErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, option, 1); configureErr != nil {
				return
			}
		}
		if network == "udp6" {
			configureErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_ALL, 0)
		} else {
			configureErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
		}
	})
	if err != nil {
		return err
	}
	return configureErr
}
