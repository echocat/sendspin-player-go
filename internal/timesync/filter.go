// Copyright 2025 Sendspin Contributors
// Copyright 2026 Gregor Noczinski
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at https://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed
// under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
// CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and limitations.

// Package timesync ports Sendspin/time-filter at revision
// 531e2707a6e82f30efe335b0f87eaa4b29390021 with its default parameters.
// Unlike the reference, Error returns float64, preserving infinity before init.
package timesync

import (
	"math"
	"math/bits"
	"sync"
)

type Filter struct {
	mu                           sync.RWMutex
	count                        int
	offset, drift, p00, p01, p11 float64
	last                         int64
	useDrift                     bool
}

func New() *Filter { return &Filter{p00: math.Inf(1)} }

// Update ignores non-monotonic samples and non-positive uncertainty.
func (f *Filter) Update(measurement, maxError, at int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if at <= f.last || maxError <= 0 {
		return
	}
	dt := float64(at - f.last)
	f.last = at
	r := math.Pow(float64(maxError)*0.5, 2)
	if f.count == 0 {
		f.count++
		f.offset = float64(measurement)
		f.p00 = r
		return
	}
	if f.count == 1 {
		f.count++
		f.drift = (float64(measurement) - f.offset) / dt
		f.offset = float64(measurement)
		f.p11 = (f.p00 + r) / (dt * dt)
		f.p00 = r
		return
	}
	offset := f.offset + f.drift*dt
	p11 := f.p11 + dt*1e-22
	p01 := f.p01 + f.p11*dt
	p00 := f.p00 + 2*f.p01*dt + f.p11*dt*dt
	residual := float64(measurement) - offset
	if f.count < 100 {
		f.count++
	} else if math.Abs(residual) > float64(maxError)*3 {
		p11 *= 4
		p01 *= 4
		p00 *= 4
	}
	u := 1 / (p00 + r)
	k0, k1 := p00*u, p01*u
	f.offset = offset + k0*residual
	f.drift += k1 * residual
	f.p11 = p11 - k1*p01
	f.p01 = p01 - k1*p00
	f.p00 = p00 - k0*p00
	f.useDrift = f.drift*f.drift > 4*f.p11
}

func (f *Filter) ServerTime(local int64) int64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	d := 0.0
	if f.useDrift {
		d = f.drift
	}
	return local + int64(math.Round(f.offset+d*float64(local-f.last)))
}
func (f *Filter) ClientTime(server int64) int64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return int64(f.clientTimeValue(server))
}

// clientTimeValue preserves the reference expression and rounding order. The
// caller holds mu; checked SDK scheduling must not cast an out-of-range float.
func (f *Filter) clientTimeValue(server int64) float64 {
	d := 0.0
	if f.useDrift {
		d = f.drift
	}
	return math.Round((float64(server) - f.offset + d*float64(f.last)) / (1 + d))
}
func (f *Filter) Error() float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return math.Round(math.Sqrt(f.p00))
}
func (f *Filter) Ready() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.count >= 3 && f.p00 <= 250000 && f.mappingUsable()
}

// Sample computes NTP offset and half RTT, truncating fractional microseconds
// toward zero. Timestamp differences and sums cannot overflow. Invalid samples
// or offsets outside int64 are ignored. Each pair shares its own clock domain.
func Sample(t1, t2, t3, t4 int64) (offset, uncertainty int64, ok bool) {
	if t4 <= t1 || t3 < t2 {
		return 0, 0, false
	}
	// Ordering is already checked: unsigned subtraction represents the exact
	// non-negative spans even when signed timestamps straddle the int64 range.
	clientSpan := uint64(t4) - uint64(t1)
	serverSpan := uint64(t3) - uint64(t2)
	if serverSpan >= clientSpan {
		return 0, 0, false
	}
	halfRTT := (clientSpan - serverSpan) / 2 // At most MaxInt64.
	if halfRTT == 0 {
		return 0, 0, false
	}
	// Exact signed 128-bit sum: t2+t3-t1-t4. Sign extension and carries keep
	// every bit without allocating big integers on the live clock-sync path.
	low, carry := bits.Add64(uint64(t2), uint64(t3), 0)
	high := (t2 >> 63) + (t3 >> 63) + int64(carry)
	low, borrow := bits.Sub64(low, uint64(t1), 0)
	high -= (t1 >> 63) + int64(borrow)
	low, borrow = bits.Sub64(low, uint64(t4), 0)
	high -= (t4 >> 63) + int64(borrow)
	negative, odd := high < 0, low&1 != 0
	low = (low >> 1) | (uint64(high) << 63)
	high >>= 1
	if negative && odd {
		low, carry = bits.Add64(low, 1, 0)
		high += int64(carry)
	}
	if high != int64(low)>>63 {
		return 0, 0, false
	}
	return int64(low), int64(halfRTT), true
}
