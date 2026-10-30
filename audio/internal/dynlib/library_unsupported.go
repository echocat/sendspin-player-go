//go:build !linux && !darwin && !windows

package dynlib

import "errors"

func load(string, bool) (uintptr, error) {
	return 0, errors.New("native libraries unsupported on this platform")
}
func symbol(uintptr, string) (uintptr, error) {
	return 0, errors.New("native libraries unsupported on this platform")
}
