//go:build linux && cgo

#include "native.h"
#include "../internal/bridge/bridge.h"
#include <pipewire/pipewire.h>
#include <spa/param/audio/format-utils.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <limits.h>

struct sendspin_pw {
    struct pw_thread_loop *loop;
    struct pw_stream *stream;
    enum pw_stream_state state;
    uintptr_t token;
    int rate, channels;
};
static pthread_once_t initialize = PTHREAD_ONCE_INIT;
static void init_library(void) { pw_init(NULL, NULL); }

static void state_changed(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
    (void)error;
    sendspin_pw *p = data;
    p->state = state;
    pw_thread_loop_signal(p->loop, false);
    if (state == PW_STREAM_STATE_ERROR || (state == PW_STREAM_STATE_UNCONNECTED && old != PW_STREAM_STATE_UNCONNECTED)) sendspinNativeFail(p->token);
}

static void process(void *data) {
    sendspin_pw *p = data;
    struct pw_buffer *buffer = pw_stream_dequeue_buffer(p->stream);
    if (!buffer) return;
    struct spa_buffer *b = buffer->buffer;
    if (b->n_datas != 1 || !b->datas[0].data || !b->datas[0].chunk) {
        sendspinNativeFail(p->token);
        pw_stream_queue_buffer(p->stream, buffer);
        return;
    }
    struct spa_data *d = &b->datas[0];
    uint32_t stride = (uint32_t)p->channels * sizeof(float);
    uint32_t frames = d->maxsize / stride;
    if (buffer->requested > 0 && buffer->requested < frames) frames = (uint32_t)buffer->requested;
    if (!frames || frames > (uint32_t)p->rate / 2) {
        d->chunk->size = 0; buffer->size = 0;
        sendspinNativeFail(p->token);
        pw_stream_queue_buffer(p->stream, buffer);
        return;
    }
    memset(d->data, 0, (size_t)frames * stride);
    struct pw_time t = {0};
    int64_t first = 0;
    if (pw_stream_get_time_n(p->stream, &t, sizeof(t)) == 0) {
        first = sendspin_native_pw_time(t.now, t.delay, t.rate.num, t.rate.denom, t.queued, t.buffered, p->rate);
    }
    sendspinNativeRender(p->token, d->data, (int)frames * p->channels, first);
    d->chunk->offset = 0; d->chunk->stride = stride; d->chunk->size = frames * stride;
    buffer->size = frames; /* pw_time.queued is consequently in stream frames. */
    if (pw_stream_queue_buffer(p->stream, buffer) < 0) sendspinNativeFail(p->token);
}

static const struct pw_stream_events events = {
    PW_VERSION_STREAM_EVENTS, .state_changed = state_changed, .process = process
};

void sendspin_pw_close(sendspin_pw *p) {
    if (!p) return;
    if (p->loop) pw_thread_loop_stop(p->loop);
    if (p->stream) pw_stream_destroy(p->stream);
    if (p->loop) pw_thread_loop_destroy(p->loop);
    free(p);
}

sendspin_pw *sendspin_pw_open(uintptr_t token, const char *target, int rate, int channels, int *error) {
    pthread_once(&initialize, init_library);
    sendspin_pw *p = calloc(1, sizeof(*p));
    *error = -ENOMEM;
    if (!p) return NULL;
    p->token = token; p->rate = rate; p->channels = channels;
    p->loop = pw_thread_loop_new("sendspin-playback", NULL);
    if (!p->loop) goto fail;
    struct pw_properties *props = pw_properties_new(PW_KEY_MEDIA_TYPE, "Audio", PW_KEY_MEDIA_CATEGORY, "Playback",
        PW_KEY_MEDIA_ROLE, "Music", PW_KEY_NODE_NAME, "sendspin-player", "node.dont-reconnect", "true", NULL);
    if (!props) goto fail;
    if (target && *target) pw_properties_set(props, PW_KEY_TARGET_OBJECT, target);
    p->stream = pw_stream_new_simple(pw_thread_loop_get_loop(p->loop), "Sendspin playback", props, &events, p);
    if (!p->stream) { *error = errno ? -errno : -EIO; goto fail; }
    uint8_t storage[1024]; struct spa_pod_builder builder = SPA_POD_BUILDER_INIT(storage, sizeof(storage));
    struct spa_audio_info_raw info = {0};
    info.format = SPA_AUDIO_FORMAT_F32; info.rate = rate; info.channels = channels;
    /* Unpositioned channels avoid inventing a speaker layout not in audio.Format. */
    info.flags = SPA_AUDIO_FLAG_UNPOSITIONED;
    const struct spa_pod *param = spa_format_audio_raw_build(&builder, SPA_PARAM_EnumFormat, &info);
    *error = pw_stream_connect(p->stream, PW_DIRECTION_OUTPUT, PW_ID_ANY,
        PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_INACTIVE, &param, 1);
    if (*error < 0) goto fail;
    *error = pw_thread_loop_start(p->loop);
    if (*error < 0) goto fail;
    pw_thread_loop_lock(p->loop);
    struct timespec deadline;
    pw_thread_loop_get_time(p->loop, &deadline, 2000000000LL);
    while (p->state != PW_STREAM_STATE_PAUSED && p->state != PW_STREAM_STATE_STREAMING && p->state != PW_STREAM_STATE_ERROR) {
        *error = pw_thread_loop_timed_wait_full(p->loop, &deadline);
        if (*error < 0) break;
    }
    if (p->state == PW_STREAM_STATE_ERROR) *error = -EIO;
    pw_thread_loop_unlock(p->loop);
    if (*error < 0) goto fail;
    return p;
fail:
    sendspin_pw_close(p); return NULL;
}
int sendspin_pw_start(sendspin_pw *p) {
    pw_thread_loop_lock(p->loop);
    int rc = pw_stream_set_active(p->stream, true);
    pw_thread_loop_unlock(p->loop); return rc;
}
int sendspin_pw_stop(sendspin_pw *p) {
    pw_thread_loop_lock(p->loop);
    int rc = pw_stream_set_active(p->stream, false);
    int flush = pw_stream_flush(p->stream, false);
    pw_thread_loop_unlock(p->loop); return rc < 0 ? rc : flush;
}
int sendspin_pw_check(sendspin_pw *p) {
    pw_thread_loop_lock(p->loop);
    int rc = p->state == PW_STREAM_STATE_ERROR || p->state == PW_STREAM_STATE_UNCONNECTED ? -EIO : 0;
    pw_thread_loop_unlock(p->loop); return rc;
}
