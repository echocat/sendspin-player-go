//go:build windows && cgo

#define COBJMACROS
#ifndef _WIN32_WINNT
#define _WIN32_WINNT 0x0A00
#endif
#include <windows.h>
#include <mmdeviceapi.h>
#include <audioclient.h>
#include <ksmedia.h>
#include <process.h>
#include <stdlib.h>
#include <limits.h>
#include "native.h"
#include "../internal/bridge/bridge.h"

struct sendspin_wa {
    uintptr_t token;
    int rate, channels;
    wchar_t *endpoint;
    HANDLE thread, ready, command_event, audio_event;
    volatile LONG command, result, failure;
};
static const CLSID enumerator_id = {0xbcde0395,0xe52f,0x467c,{0x8e,0x3d,0xc4,0x57,0x92,0x91,0x69,0x2e}};
static const IID enumerator_iid = {0xa95664d2,0x9614,0x4f35,{0xa7,0x46,0xde,0x8d,0xb6,0x36,0x17,0xe6}};
static const IID client_iid = {0x1cb9ad4c,0xdbfa,0x4c32,{0xb1,0x78,0xc2,0xf5,0x68,0xa7,0x03,0xb2}};
static const IID render_iid = {0xf294acfc,0x3146,0x4483,{0xa7,0xbf,0xad,0xdc,0xa7,0xc2,0x60,0xe2}};
static const IID clock_iid = {0xcd63314f,0x3fba,0x4a1b,{0x81,0x2c,0xef,0x96,0x35,0x87,0x28,0xe7}};
static const GUID float_id = {3,0,0x10,{0x80,0,0,0xaa,0,0x38,0x9b,0x71}};

static unsigned __stdcall worker(void *data) {
    sendspin_wa *p = data;
    IMMDeviceEnumerator *enumerator = NULL; IMMDevice *device = NULL;
    IAudioClient *client = NULL; IAudioRenderClient *render = NULL; IAudioClock *clock = NULL;
    int com = 0, running = 0, notified = 0;
    UINT32 capacity = 0; UINT64 frequency = 0, submitted = 0, previous = 0;
    HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (FAILED(hr)) goto done;
    com = 1;
#define CHECK(call) do { hr = (call); if (FAILED(hr)) goto done; } while (0)
    CHECK(CoCreateInstance(&enumerator_id, NULL, CLSCTX_ALL, &enumerator_iid, (void **)&enumerator));
    if (p->endpoint) CHECK(IMMDeviceEnumerator_GetDevice(enumerator, p->endpoint, &device));
    else CHECK(IMMDeviceEnumerator_GetDefaultAudioEndpoint(enumerator, eRender, eMultimedia, &device));
    CHECK(IMMDevice_Activate(device, &client_iid, CLSCTX_ALL, NULL, (void **)&client));
    WAVEFORMATEXTENSIBLE format = {0};
    format.Format.wFormatTag = WAVE_FORMAT_EXTENSIBLE; format.Format.nChannels = (WORD)p->channels;
    format.Format.nSamplesPerSec = p->rate; format.Format.wBitsPerSample = 32;
    format.Format.nBlockAlign = (WORD)(p->channels * sizeof(float));
    format.Format.nAvgBytesPerSec = p->rate * format.Format.nBlockAlign;
    format.Format.cbSize = sizeof(format) - sizeof(WAVEFORMATEX);
    format.Samples.wValidBitsPerSample = 32; format.SubFormat = float_id;
    switch (p->channels) {
    case 1: format.dwChannelMask = SPEAKER_FRONT_CENTER; break;
    case 2: format.dwChannelMask = SPEAKER_FRONT_LEFT | SPEAKER_FRONT_RIGHT; break;
    case 4: format.dwChannelMask = SPEAKER_FRONT_LEFT | SPEAKER_FRONT_RIGHT | SPEAKER_BACK_LEFT | SPEAKER_BACK_RIGHT; break;
    case 6: format.dwChannelMask = KSAUDIO_SPEAKER_5POINT1; break;
    case 8: format.dwChannelMask = KSAUDIO_SPEAKER_7POINT1_SURROUND; break;
    default: hr = E_INVALIDARG; goto done;
    }
    CHECK(IAudioClient_Initialize(client, AUDCLNT_SHAREMODE_SHARED,
        AUDCLNT_STREAMFLAGS_EVENTCALLBACK | AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY | AUDCLNT_STREAMFLAGS_NOPERSIST,
        500000, 0, &format.Format, NULL));
    CHECK(IAudioClient_GetBufferSize(client, &capacity));
    if (!capacity || capacity > (UINT32)p->rate / 2) { hr = E_FAIL; goto done; }
    CHECK(IAudioClient_SetEventHandle(client, p->audio_event));
    CHECK(IAudioClient_GetService(client, &render_iid, (void **)&render));
    CHECK(IAudioClient_GetService(client, &clock_iid, (void **)&clock));
    CHECK(IAudioClock_GetFrequency(clock, &frequency));
    if (!frequency) { hr = E_FAIL; goto done; }
    InterlockedExchange(&p->result, S_OK); SetEvent(p->ready);
    HANDLE waits[] = {p->command_event, p->audio_event};
    for (;;) {
        DWORD which = WaitForMultipleObjects(2, waits, FALSE, INFINITE);
        if (which == WAIT_OBJECT_0) {
            LONG command = InterlockedExchange(&p->command, 0);
            if (command == 3) break;
            hr = S_OK;
            if (command == 1 && !running) {
                hr = IAudioClient_Reset(client);
                BYTE *out = NULL;
                if (SUCCEEDED(hr)) hr = IAudioRenderClient_GetBuffer(render, capacity, &out);
                if (SUCCEEDED(hr)) hr = IAudioRenderClient_ReleaseBuffer(render, capacity, AUDCLNT_BUFFERFLAGS_SILENT);
                submitted = capacity; previous = 0;
                if (SUCCEEDED(hr)) hr = IAudioClient_Start(client);
                if (SUCCEEDED(hr)) running = 1;
            } else if (command == 2) {
                if (running) hr = IAudioClient_Stop(client);
                running = 0;
                if (SUCCEEDED(hr)) hr = IAudioClient_Reset(client);
            }
            if (FAILED(hr)) InterlockedExchange(&p->failure, hr);
            InterlockedExchange(&p->result, hr); SetEvent(p->ready);
            continue;
        }
        if (which != WAIT_OBJECT_0 + 1) { hr = E_FAIL; break; }
        if (!running) continue;
        UINT32 padding = 0;
        hr = IAudioClient_GetCurrentPadding(client, &padding);
        if (FAILED(hr) || padding > capacity) { if (SUCCEEDED(hr)) hr = E_FAIL; goto failed; }
        UINT32 frames = capacity - padding;
        if (!frames) continue;
        UINT64 position = 0, qpc = 0;
        hr = IAudioClock_GetPosition(clock, &position, &qpc);
        if (FAILED(hr)) goto failed;
        int64_t first = 0;
        if (hr == S_OK && position > 0 && qpc > 0 && qpc <= INT64_MAX / 100) {
            first = sendspin_native_wa_time(submitted, p->rate, position, frequency, qpc, previous);
            if (first <= 0) { hr = E_FAIL; goto failed; }
            previous = position;
        }
        BYTE *out = NULL;
        hr = IAudioRenderClient_GetBuffer(render, frames, &out);
        if (FAILED(hr)) goto failed;
        /* Clock-zero/S_FALSE startup never invents a timestamp or releases media. */
        if (first > 0) sendspinNativeRender(p->token, (float *)out, (int)frames * p->channels, first);
        hr = IAudioRenderClient_ReleaseBuffer(render, frames, first > 0 ? 0 : AUDCLNT_BUFFERFLAGS_SILENT);
        if (FAILED(hr)) goto failed;
        submitted += frames;
        continue;
failed:
        InterlockedExchange(&p->failure, hr);
        if (!notified) { sendspinNativeFail(p->token); notified = 1; }
        IAudioClient_Stop(client); running = 0;
    }
done:
    if (FAILED(hr)) InterlockedExchange(&p->failure, hr);
    InterlockedExchange(&p->result, hr); SetEvent(p->ready);
    if (running && client) IAudioClient_Stop(client);
    if (clock) IAudioClock_Release(clock);
    if (render) IAudioRenderClient_Release(render);
    if (client) IAudioClient_Release(client);
    if (device) IMMDevice_Release(device);
    if (enumerator) IMMDeviceEnumerator_Release(enumerator);
    if (com) CoUninitialize();
    return 0;
#undef CHECK
}

void sendspin_wa_close(sendspin_wa *p) {
    if (!p) return;
    if (p->thread) {
        InterlockedExchange(&p->command, 3); SetEvent(p->command_event);
        WaitForSingleObject(p->thread, INFINITE); CloseHandle(p->thread);
    }
    if (p->ready) CloseHandle(p->ready);
    if (p->command_event) CloseHandle(p->command_event);
    if (p->audio_event) CloseHandle(p->audio_event);
    free(p->endpoint); free(p);
}
sendspin_wa *sendspin_wa_open(uintptr_t token, const char *endpoint, int rate, int channels, int32_t *error) {
    sendspin_wa *p = calloc(1, sizeof(*p)); *error = E_OUTOFMEMORY;
    if (!p) return NULL;
    p->token = token; p->rate = rate; p->channels = channels;
    if (endpoint && *endpoint) {
        int n = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, endpoint, -1, NULL, 0);
        if (!n) { *error = E_INVALIDARG; goto fail; }
        p->endpoint = calloc((size_t)n, sizeof(wchar_t));
        if (!p->endpoint) goto fail;
        if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, endpoint, -1, p->endpoint, n)) { *error = E_INVALIDARG; goto fail; }
    }
    p->ready = CreateEventW(NULL, TRUE, FALSE, NULL);
    p->command_event = CreateEventW(NULL, FALSE, FALSE, NULL);
    p->audio_event = CreateEventW(NULL, FALSE, FALSE, NULL);
    if (!p->ready || !p->command_event || !p->audio_event) goto fail;
    p->thread = (HANDLE)_beginthreadex(NULL, 0, worker, p, 0, NULL);
    if (!p->thread) goto fail;
    if (WaitForSingleObject(p->ready, INFINITE) != WAIT_OBJECT_0) { *error = E_FAIL; goto fail; }
    *error = (int32_t)InterlockedCompareExchange(&p->failure, 0, 0);
    if (FAILED(*error)) goto fail;
    return p;
fail:
    sendspin_wa_close(p); return NULL;
}
static int32_t command(sendspin_wa *p, LONG action) {
    LONG failure = InterlockedCompareExchange(&p->failure, 0, 0);
    if (FAILED(failure)) return failure;
    ResetEvent(p->ready); InterlockedExchange(&p->command, action); SetEvent(p->command_event);
    HANDLE waits[] = {p->ready, p->thread};
    if (WaitForMultipleObjects(2, waits, FALSE, INFINITE) != WAIT_OBJECT_0) return E_FAIL;
    return (int32_t)InterlockedCompareExchange(&p->result, 0, 0);
}
int32_t sendspin_wa_start(sendspin_wa *p) { return command(p, 1); }
int32_t sendspin_wa_stop(sendspin_wa *p) { return command(p, 2); }
int32_t sendspin_wa_check(sendspin_wa *p) { return (int32_t)InterlockedCompareExchange(&p->failure, 0, 0); }
