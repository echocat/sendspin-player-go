package sendspin

import "golang.org/x/sys/windows"

func socketIPv6Only(fd uintptr) (bool, error) {
	value, err := windows.GetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, windows.IPV6_V6ONLY)
	return value != 0, err
}
