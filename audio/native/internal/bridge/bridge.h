#ifndef SENDSPIN_NATIVE_BRIDGE_H
#define SENDSPIN_NATIVE_BRIDGE_H
#include <stdint.h>
int64_t sendspin_native_now(void);
int64_t sendspin_native_pw_time(int64_t now, int64_t delay, uint32_t num, uint32_t denom, uint64_t queued, uint64_t buffered, uint32_t rate);
int64_t sendspin_native_wa_time(uint64_t submitted, uint32_t rate, uint64_t position, uint64_t frequency, uint64_t qpc, uint64_t previous);
int64_t sendspin_native_ca_time(uint64_t host_ns, uint32_t latency, double rate);
uint32_t sendspin_native_pw_frames(uint64_t requested, uint32_t matched, uint64_t duration, uint32_t num, uint32_t denom, uint32_t rate, uint32_t capacity);
void sendspinNativeRender(uintptr_t token, float *out, int count, int64_t first);
void sendspinNativeFail(uintptr_t token);
#endif
