//go:build cgo && (linux || windows || darwin)

#include "bridge.h"
#include <limits.h>
#include <math.h>

static int64_t timestamp(long double ns) {
    return isfinite(ns) && ns > 0 && ns < INT64_MAX ? (int64_t)ns : 0;
}
uint32_t sendspin_native_pw_frames(uint64_t requested, uint32_t matched, uint64_t duration, uint32_t num, uint32_t denom, uint32_t rate, uint32_t capacity) {
    if (!capacity) return 0;
    if (!requested) requested = matched;
    if (!requested) {
        /* Buffer allocation capacity is not a render quantum. Older PipeWire
           versions leave pw_buffer.requested zero for equal-rate streams. Use
           actual graph IO duration, never the whole allocation or a sleep. */
        if (!duration || !num || !denom || !rate || duration > UINT64_MAX / num) return 0;
        uint64_t frames = duration * num;
        if (frames > UINT64_MAX / rate) return 0;
        frames *= rate;
        /* Fractional resampler demand needs RateMatch/requested metadata. */
        if (frames % denom) return 0;
        requested = frames / denom;
    }
    return requested < capacity ? (uint32_t)requested : capacity;
}
int64_t sendspin_native_pw_time(int64_t now, int64_t delay, uint32_t num, uint32_t denom, uint64_t queued, uint64_t buffered, uint32_t rate) {
    if (now <= 0 || !num || !denom || !rate || queued > (uint64_t)rate * 2 || buffered > (uint64_t)rate * 2) return 0;
    return timestamp((long double)now + (long double)(delay > 0 ? delay : 0) * 1000000000.0L * num / denom
        + ((long double)queued + buffered) * 1000000000.0L / rate);
}
int64_t sendspin_native_wa_time(uint64_t submitted, uint32_t rate, uint64_t position, uint64_t frequency, uint64_t qpc, uint64_t previous) {
    if (!rate || !frequency || !position || !qpc || qpc > INT64_MAX / 100 || position < previous) return 0;
    long double queued = (long double)submitted / rate - (long double)position / frequency;
    if (queued < 0 || queued > 2) return 0;
    return timestamp((long double)qpc * 100 + queued * 1000000000.0L);
}
int64_t sendspin_native_ca_time(uint64_t host_ns, uint32_t latency, double rate) {
    if (!host_ns || !isfinite(rate) || rate <= 0 || latency / rate > 2) return 0;
    return timestamp((long double)host_ns + (long double)latency * 1000000000.0L / rate);
}
