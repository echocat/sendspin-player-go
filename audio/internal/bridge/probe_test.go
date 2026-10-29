//go:build (linux || windows || darwin) && (amd64 || arm64)

package bridge

import (
	"sync"

	"github.com/ebitengine/purego"
)

var probeCallbacks struct {
	once   sync.Once
	render func(uintptr, *float32, int32, int64)
	fail   func(uintptr)
}

func initProbe() {
	probeCallbacks.once.Do(func() {
		purego.RegisterFunc(&probeCallbacks.render, purego.NewCallback(func(id uintptr, out *float32, n int32, first int64) { Render(id, out, int(n), first) }))
		purego.RegisterFunc(&probeCallbacks.fail, purego.NewCallback(func(id uintptr) { Fail(id) }))
	})
}
func probe(id uintptr, count int, first int64) []float32 {
	initProbe()
	out := make([]float32, count)
	probeCallbacks.render(id, &out[0], int32(count), first)
	return out
}
func probeFailure(id uintptr) { initProbe(); probeCallbacks.fail(id) }
func probePW(now, delay int64, num, denom uint32, queued, buffered uint64, rate uint32) int64 {
	return PWTime(now, delay, num, denom, queued, buffered, rate)
}
func probeWA(submitted uint64, rate uint32, position, frequency, qpc, previous uint64) int64 {
	return WATime(submitted, rate, position, frequency, qpc, previous)
}
func probeCA(host uint64, latency uint32, rate float64) int64 { return CATime(host, latency, rate) }
func probePWFrames(requested uint64, matched uint32, duration uint64, num, denom, rate, capacity uint32) uint32 {
	return PWFrames(requested, matched, duration, num, denom, rate, capacity)
}
