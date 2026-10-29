//go:build cgo && (linux || windows || darwin)

package bridge

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import "unsafe"

// probe invokes precisely the native ABI using C-owned scratch storage. It never
// initializes an audio library, opens hardware or connects to an audio server.
func probe(token uintptr, count int, first int64) []float32 {
	data := C.calloc(C.size_t(count), C.size_t(4))
	defer C.free(data)
	C.sendspinNativeRender(C.uintptr_t(token), (*C.float)(data), C.int(count), C.int64_t(first))
	return append([]float32(nil), unsafe.Slice((*float32)(data), count)...)
}
func probeFailure(token uintptr) { C.sendspinNativeFail(C.uintptr_t(token)) }

func probePW(now, delay int64, num, denom uint32, queued, buffered uint64, rate uint32) int64 {
	return int64(C.sendspin_native_pw_time(C.int64_t(now), C.int64_t(delay), C.uint32_t(num), C.uint32_t(denom), C.uint64_t(queued), C.uint64_t(buffered), C.uint32_t(rate)))
}
func probeWA(submitted uint64, rate uint32, position, frequency, qpc, previous uint64) int64 {
	return int64(C.sendspin_native_wa_time(C.uint64_t(submitted), C.uint32_t(rate), C.uint64_t(position), C.uint64_t(frequency), C.uint64_t(qpc), C.uint64_t(previous)))
}
func probeCA(host uint64, latency uint32, rate float64) int64 {
	return int64(C.sendspin_native_ca_time(C.uint64_t(host), C.uint32_t(latency), C.double(rate)))
}

func probePWFrames(requested uint64, matched uint32, duration uint64, num, denom, rate, capacity uint32) uint32 {
	return uint32(C.sendspin_native_pw_frames(C.uint64_t(requested), C.uint32_t(matched), C.uint64_t(duration), C.uint32_t(num), C.uint32_t(denom), C.uint32_t(rate), C.uint32_t(capacity)))
}
