//go:build cgo && (linux || windows || darwin)

#include "bridge.h"
#include <limits.h>
#if defined(_WIN32)
#include <windows.h>
int64_t sendspin_native_now(void) {
    LARGE_INTEGER count, freq;
    if (!QueryPerformanceCounter(&count) || !QueryPerformanceFrequency(&freq) || freq.QuadPart <= 0) return 0;
    long double ns = (long double)count.QuadPart * 1000000000.0L / freq.QuadPart;
    return ns > 0 && ns < INT64_MAX ? (int64_t)ns : 0;
}
#elif defined(__APPLE__)
#include <mach/mach_time.h>
int64_t sendspin_native_now(void) {
    mach_timebase_info_data_t info;
    if (mach_timebase_info(&info) != KERN_SUCCESS || info.denom == 0) return 0;
    long double ns = (long double)mach_absolute_time() * info.numer / info.denom;
    return ns > 0 && ns < INT64_MAX ? (int64_t)ns : 0;
}
#else
#include <time.h>
int64_t sendspin_native_now(void) {
    struct timespec now;
    if (clock_gettime(CLOCK_MONOTONIC, &now) != 0) return 0;
    return (int64_t)now.tv_sec * 1000000000 + now.tv_nsec;
}
#endif
