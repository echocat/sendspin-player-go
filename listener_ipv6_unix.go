//go:build unix

package sendspin

import "golang.org/x/sys/unix"

func socketIPv6Only(fd uintptr) (bool, error) {
	value, err := unix.GetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_V6ONLY)
	return value != 0, err
}
