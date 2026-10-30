//go:build !linux

package mdnstest

import (
	"errors"
	"syscall"
)

func multicastSocketControl(_, _ string, _ syscall.RawConn) error {
	return errors.New("mDNS E2E sockets require the private Linux container")
}
