#ifndef SENDSPIN_ALSA_NATIVE_H
#define SENDSPIN_ALSA_NATIVE_H
#include <alsa/asoundlib.h>
#include <stdint.h>
#include <errno.h>

typedef struct {
    int encoding;
    int hardware;
    uint64_t buffer_frames;
    uint64_t period_frames;
} sendspin_alsa_config;

typedef struct {
    int state;
    int64_t delay;
    int64_t stamp_ns;
    int64_t now_ns;
} sendspin_alsa_status;

int sendspin_alsa_configure(snd_pcm_t *pcm, unsigned int rate, unsigned int channels,
    unsigned int buffer_us, unsigned int period_us, sendspin_alsa_config *result);
int sendspin_alsa_snapshot(snd_pcm_t *pcm, sendspin_alsa_status *result);
#endif
