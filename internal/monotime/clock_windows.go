//go:build windows

package monotime

import (
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var kernel = windows.NewLazySystemDLL("kernel32.dll")
var queryCounter = kernel.NewProc("QueryPerformanceCounter")
var queryFrequency = kernel.NewProc("QueryPerformanceFrequency")

type performanceClock struct {
	anchor            time.Time
	origin, frequency int64
	last              atomic.Int64
}

func readCounter() (int64, bool) {
	var value int64
	ok, _, _ := queryCounter.Call(uintptr(unsafe.Pointer(&value)))
	return value, ok != 0
}

func newPerformanceClock() *performanceClock {
	var frequency int64
	if queryCounter.Find() != nil || queryFrequency.Find() != nil {
		return nil
	}
	success, _, _ := queryFrequency.Call(uintptr(unsafe.Pointer(&frequency)))
	if success == 0 || frequency <= 0 {
		return nil
	}
	initial, ok := readCounter()
	if !ok {
		return nil
	}
	previous := time.Now()
	// Correlate at a runtime-clock tick edge, rather than sampling an arbitrary
	// point inside its quantization interval. This is a local correlation, not
	// a hardware/DAC accuracy certificate. Bound initialization with QPC itself.
	for {
		before, ok := readCounter()
		if !ok {
			return nil
		}
		now := time.Now()
		after, ok := readCounter()
		if !ok {
			return nil
		}
		if now.After(previous) || counterDuration(after-initial, frequency) >= 50*time.Millisecond {
			return &performanceClock{anchor: now, origin: before + (after-before)/2, frequency: frequency}
		}
	}
}

var performance = newPerformanceClock()

func Source() string {
	if performance == nil {
		return "go-runtime-monotonic-fallback"
	}
	return "windows-query-performance-counter"
}

func Now() time.Time {
	p := performance
	if p == nil {
		return time.Now()
	}
	value, ok := readCounter()
	elapsed := time.Since(p.anchor)
	if ok {
		elapsed = counterDuration(value-p.origin, p.frequency)
	}
	// Preserve nondecreasing time even for reordered concurrent QPC calls or
	// a failed native read. Do not synthesize an extra tick/positive interval.
	for {
		last := p.last.Load()
		if int64(elapsed) <= last {
			return p.anchor.Add(time.Duration(last))
		}
		if p.last.CompareAndSwap(last, int64(elapsed)) {
			return p.anchor.Add(elapsed)
		}
	}
}
