//go:build windows

package bridge

import (
	"sync"

	"github.com/echocat/sendspin-player-go/audio/internal/dynlib"
)

var windowsClock struct {
	once      sync.Once
	counter   func(*int64) int32
	frequency int64
}

func Now() int64 {
	windowsClock.once.Do(func() {
		lib, err := dynlib.Open("", "kernel32.dll")
		if err != nil {
			return
		}
		var frequency func(*int64) int32
		if lib.Bind(&frequency, "QueryPerformanceFrequency") != nil || lib.Bind(&windowsClock.counter, "QueryPerformanceCounter") != nil {
			return
		}
		if frequency(&windowsClock.frequency) == 0 {
			windowsClock.frequency = 0
		}
	})
	if windowsClock.counter == nil || windowsClock.frequency <= 0 {
		return 0
	}
	var v int64
	if windowsClock.counter(&v) == 0 {
		return 0
	}
	return timestamp(float64(v) * 1e9 / float64(windowsClock.frequency))
}
