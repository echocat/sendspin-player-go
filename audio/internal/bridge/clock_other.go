//go:build !linux && !windows && !darwin

package bridge

func Now() int64 { return 0 }
