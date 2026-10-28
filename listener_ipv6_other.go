//go:build !unix && !windows

package sendspin

import "errors"

func socketIPv6Only(uintptr) (bool, error) {
	return false, errors.New("sendspin: cannot inspect IPv6 listener on this platform")
}
