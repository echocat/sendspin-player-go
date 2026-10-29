//go:build linux

package bridge

import "golang.org/x/sys/unix"

func Now() int64 {
	var t unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &t) != nil {
		return 0
	}
	return t.Nano()
}
