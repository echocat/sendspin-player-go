//go:build linux && cgo

#include "native.h"
#include <errno.h>
#include <time.h>

int sendspin_alsa_configure(snd_pcm_t *pcm, unsigned int rate, unsigned int channels,
    unsigned int buffer_us, unsigned int period_us, sendspin_alsa_config *result) {
    snd_pcm_hw_params_t *hw;
    snd_pcm_sw_params_t *sw;
    snd_pcm_uframes_t buffer, period, boundary;
    snd_pcm_format_t formats[] = {SND_PCM_FORMAT_FLOAT_LE, SND_PCM_FORMAT_S32_LE,
        SND_PCM_FORMAT_S24_LE, SND_PCM_FORMAT_S24_3LE, SND_PCM_FORMAT_S16_LE};
    snd_pcm_format_t selected = SND_PCM_FORMAT_UNKNOWN;
    int encoding = 0;
    int rc, direction = 0;
    snd_pcm_hw_params_alloca(&hw);
    snd_pcm_sw_params_alloca(&sw);
#define CHECK(call) do { rc = (call); if (rc < 0) return rc; } while (0)
    CHECK(snd_pcm_hw_params_any(pcm, hw));
    CHECK(snd_pcm_hw_params_set_access(pcm, hw, SND_PCM_ACCESS_RW_INTERLEAVED));
    CHECK(snd_pcm_hw_params_set_rate_resample(pcm, hw, 0));
    CHECK(snd_pcm_hw_params_set_channels(pcm, hw, channels));
    CHECK(snd_pcm_hw_params_set_rate(pcm, hw, rate, 0));
    for (unsigned int i = 0; i < sizeof(formats)/sizeof(formats[0]); ++i) {
        if (snd_pcm_hw_params_test_format(pcm, hw, formats[i]) == 0) {
            selected = formats[i]; encoding = (int)i + 1; break;
        }
    }
    if (selected == SND_PCM_FORMAT_UNKNOWN) return -EINVAL;
    CHECK(snd_pcm_hw_params_set_format(pcm, hw, selected));
    CHECK(snd_pcm_hw_params_set_period_time_near(pcm, hw, &period_us, &direction));
    direction = 0;
    CHECK(snd_pcm_hw_params_set_buffer_time_near(pcm, hw, &buffer_us, &direction));
    CHECK(snd_pcm_hw_params(pcm, hw));
    CHECK(snd_pcm_hw_params_get_buffer_size(hw, &buffer));
    direction = 0;
    CHECK(snd_pcm_hw_params_get_period_size(hw, &period, &direction));
    CHECK(snd_pcm_sw_params_current(pcm, sw));
    CHECK(snd_pcm_sw_params_get_boundary(sw, &boundary));
    CHECK(snd_pcm_sw_params_set_start_threshold(pcm, sw, boundary));
    CHECK(snd_pcm_sw_params_set_stop_threshold(pcm, sw, buffer));
    CHECK(snd_pcm_sw_params_set_silence_threshold(pcm, sw, 0));
    CHECK(snd_pcm_sw_params_set_silence_size(pcm, sw, 0));
    CHECK(snd_pcm_sw_params_set_avail_min(pcm, sw, period));
    CHECK(snd_pcm_sw_params_set_tstamp_mode(pcm, sw, SND_PCM_TSTAMP_ENABLE));
    CHECK(snd_pcm_sw_params_set_tstamp_type(pcm, sw, SND_PCM_TSTAMP_TYPE_MONOTONIC));
    CHECK(snd_pcm_sw_params(pcm, sw));
    CHECK(snd_pcm_prepare(pcm));
    result->encoding = encoding;
    result->hardware = snd_pcm_type(pcm) == SND_PCM_TYPE_HW;
    result->buffer_frames = buffer;
    result->period_frames = period;
    return 0;
#undef CHECK
}

int sendspin_alsa_snapshot(snd_pcm_t *pcm, sendspin_alsa_status *result) {
    snd_pcm_status_t *status;
    snd_htimestamp_t stamp;
    struct timespec now;
    snd_pcm_status_alloca(&status);
    int rc = snd_pcm_status(pcm, status);
    if (rc < 0) return rc;
    snd_pcm_status_get_htstamp(status, &stamp);
    if (clock_gettime(CLOCK_MONOTONIC, &now) < 0) return -errno;
    result->state = snd_pcm_status_get_state(status);
    result->delay = snd_pcm_status_get_delay(status);
    result->stamp_ns = (int64_t)stamp.tv_sec * 1000000000 + stamp.tv_nsec;
    result->now_ns = (int64_t)now.tv_sec * 1000000000 + now.tv_nsec;
    return 0;
}
