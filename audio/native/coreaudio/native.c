//go:build darwin && cgo

#include "native.h"
#include "../internal/bridge/bridge.h"
#include <AudioUnit/AudioUnit.h>
#include <CoreAudio/CoreAudio.h>
#include <CoreAudio/HostTime.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>
#include <limits.h>
#include <math.h>
#include <stdatomic.h>
#include <time.h>

struct sendspin_ca {
    AudioUnit unit;
    AudioDeviceID device;
    uintptr_t token;
    atomic_uint callbacks;
    atomic_int closing;
    int channels, rate, initialized, started;
    UInt32 latency;
    Float64 nominal_rate;
};
static OSStatus get_property(AudioObjectID object, AudioObjectPropertySelector selector, AudioObjectPropertyScope scope, void *value, UInt32 size) {
    AudioObjectPropertyAddress address = {selector, scope, kAudioObjectPropertyElementMain};
    return AudioObjectGetPropertyData(object, &address, 0, NULL, &size, value);
}
int32_t sendspin_ca_check(sendspin_ca *p) {
    UInt32 alive = 0, latency = 0; Float64 rate = 0;
    OSStatus rc = get_property(p->device, kAudioDevicePropertyDeviceIsAlive, kAudioObjectPropertyScopeGlobal, &alive, sizeof(alive));
    if (rc) return rc;
    if (!alive) return kAudioHardwareBadDeviceError;
    rc = get_property(p->device, kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal, &rate, sizeof(rate));
    if (rc) return rc;
    rc = get_property(p->device, kAudioDevicePropertyLatency, kAudioObjectPropertyScopeOutput, &latency, sizeof(latency));
    if (rc) return rc;
    return rate == p->nominal_rate && latency == p->latency ? noErr : kAudioHardwareBadDeviceError;
}
static OSStatus process(void *data, AudioUnitRenderActionFlags *flags, const AudioTimeStamp *stamp,
    UInt32 bus, UInt32 frames, AudioBufferList *buffers) {
    (void)flags; (void)bus;
    sendspin_ca *p = data;
    atomic_fetch_add(&p->callbacks, 1);
    if (atomic_load(&p->closing)) {
        if (buffers) for (UInt32 i = 0; i < buffers->mNumberBuffers && i < 8; i++) {
            if (buffers->mBuffers[i].mData) memset(buffers->mBuffers[i].mData, 0, buffers->mBuffers[i].mDataByteSize);
        }
        atomic_fetch_sub(&p->callbacks, 1); return noErr;
    }
    if (!buffers) { sendspinNativeFail(p->token); atomic_fetch_sub(&p->callbacks, 1); return kAudio_ParamError; }
    /* The configured client format is one interleaved native-endian float buffer. */
    if (buffers->mNumberBuffers != 1 || frames == 0 || frames > (UInt32)p->rate / 2 ||
        !buffers->mBuffers[0].mData || buffers->mBuffers[0].mNumberChannels != (UInt32)p->channels ||
        buffers->mBuffers[0].mDataByteSize < (size_t)frames * p->channels * sizeof(float)) {
        for (UInt32 i = 0; i < buffers->mNumberBuffers && i < 8; i++) {
            if (buffers->mBuffers[i].mData) memset(buffers->mBuffers[i].mData, 0, buffers->mBuffers[i].mDataByteSize);
        }
        sendspinNativeFail(p->token); atomic_fetch_sub(&p->callbacks, 1); return kAudioUnitErr_FormatNotSupported;
    }
    int64_t first = 0;
    if (stamp && (stamp->mFlags & kAudioTimeStampHostTimeValid)) {
        UInt64 ns = AudioConvertHostTimeToNanos(stamp->mHostTime);
        first = sendspin_native_ca_time(ns, p->latency, p->nominal_rate);
    }
    sendspinNativeRender(p->token, buffers->mBuffers[0].mData, (int)frames * p->channels, first);
    atomic_fetch_sub(&p->callbacks, 1);
    return noErr;
}

int32_t sendspin_ca_stop(sendspin_ca *p) {
    OSStatus rc = noErr;
    if (p->started) rc = AudioOutputUnitStop(p->unit);
    if (!rc) p->started = 0;
    OSStatus reset = AudioUnitReset(p->unit, kAudioUnitScope_Global, 0);
    return rc ? rc : reset;
}
int32_t sendspin_ca_close(sendspin_ca *p) {
    if (!p) return 0;
    OSStatus rc = noErr;
    OSStatus disposed = noErr;
    atomic_store(&p->closing, 1);
    if (p->unit) {
        if (p->started) rc = AudioOutputUnitStop(p->unit);
        if (p->initialized) { OSStatus r = AudioUnitUninitialize(p->unit); if (!rc) rc = r; }
        disposed = AudioComponentInstanceDispose(p->unit); if (!rc) rc = disposed;
    }
    /* Detach/join Go callbacks even if a broken native unit refuses disposal.
       In that failure case the tiny C trampoline remains alive and emits only
       zeros: leaking a failed native allocation is safer than a dangling token. */
    while (atomic_load(&p->callbacks)) { struct timespec delay = {0, 1000000}; nanosleep(&delay, NULL); }
    if (!disposed) free(p);
    return rc;
}
sendspin_ca *sendspin_ca_open(uintptr_t token, const char *uid, int rate, int channels, int32_t *error) {
    sendspin_ca *p = calloc(1, sizeof(*p)); *error = kAudioUnitErr_FailedInitialization;
    if (!p) return NULL;
    atomic_init(&p->callbacks, 0); atomic_init(&p->closing, 0);
    p->token = token; p->rate = rate; p->channels = channels;
#define CHECK(call) do { *error = (call); if (*error) goto fail; } while (0)
    if (uid && *uid) {
        CFStringRef name = CFStringCreateWithCString(kCFAllocatorDefault, uid, kCFStringEncodingUTF8);
        if (!name) { *error = kAudio_ParamError; goto fail; }
        AudioValueTranslation translation = {&name, sizeof(name), &p->device, sizeof(p->device)};
        OSStatus rc = get_property(kAudioObjectSystemObject, kAudioHardwarePropertyTranslateUIDToDevice,
            kAudioObjectPropertyScopeGlobal, &translation, sizeof(translation));
        CFRelease(name);
        CHECK(rc);
    } else {
        CHECK(get_property(kAudioObjectSystemObject, kAudioHardwarePropertyDefaultOutputDevice,
            kAudioObjectPropertyScopeGlobal, &p->device, sizeof(p->device)));
    }
    if (p->device == kAudioObjectUnknown) { *error = kAudioHardwareBadDeviceError; goto fail; }
    CHECK(get_property(p->device, kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal,
        &p->nominal_rate, sizeof(p->nominal_rate)));
    CHECK(get_property(p->device, kAudioDevicePropertyLatency, kAudioObjectPropertyScopeOutput, &p->latency, sizeof(p->latency)));
    if (!isfinite(p->nominal_rate) || p->nominal_rate < 8000 || p->nominal_rate > 384000 || p->latency > p->nominal_rate * 2) { *error = kAudio_ParamError; goto fail; }
    AudioComponentDescription description = {kAudioUnitType_Output, kAudioUnitSubType_HALOutput, kAudioUnitManufacturer_Apple, 0, 0};
    AudioComponent component = AudioComponentFindNext(NULL, &description);
    if (!component) { *error = kAudioUnitErr_InvalidProperty; goto fail; }
    CHECK(AudioComponentInstanceNew(component, &p->unit));
    UInt32 enabled = 0;
    CHECK(AudioUnitSetProperty(p->unit, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Input, 1, &enabled, sizeof(enabled)));
    enabled = 1;
    CHECK(AudioUnitSetProperty(p->unit, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Output, 0, &enabled, sizeof(enabled)));
    CHECK(AudioUnitSetProperty(p->unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0, &p->device, sizeof(p->device)));
    AudioStreamBasicDescription format = {0};
    format.mSampleRate = rate; format.mFormatID = kAudioFormatLinearPCM;
    format.mFormatFlags = kAudioFormatFlagsNativeFloatPacked;
    format.mFramesPerPacket = 1; format.mChannelsPerFrame = channels; format.mBitsPerChannel = 32;
    format.mBytesPerFrame = channels * sizeof(float); format.mBytesPerPacket = format.mBytesPerFrame;
    CHECK(AudioUnitSetProperty(p->unit, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Input, 0, &format, sizeof(format)));
    UInt32 maximum = rate / 2 < 32768 ? rate / 2 : 32768;
    CHECK(AudioUnitSetProperty(p->unit, kAudioUnitProperty_MaximumFramesPerSlice, kAudioUnitScope_Global, 0, &maximum, sizeof(maximum)));
    AURenderCallbackStruct callback = {process, p};
    CHECK(AudioUnitSetProperty(p->unit, kAudioUnitProperty_SetRenderCallback, kAudioUnitScope_Input, 0, &callback, sizeof(callback)));
    CHECK(AudioUnitInitialize(p->unit)); p->initialized = 1;
    return p;
fail:
    sendspin_ca_close(p); return NULL;
#undef CHECK
}
int32_t sendspin_ca_start(sendspin_ca *p) {
    OSStatus rc = sendspin_ca_check(p);
    if (!rc) rc = AudioOutputUnitStart(p->unit);
    if (!rc) p->started = 1;
    return rc;
}
