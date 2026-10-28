//go:build linux && cgo

package alsa

/*
#cgo pkg-config: alsa
#include <stdlib.h>
#include "native.h"
*/
import "C"

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
)

type nativePCM struct{ handle *C.snd_pcm_t }

func alsaError(op string, code C.int) error {
	switch code {
	case -C.EAGAIN:
		return errAgain
	case -C.EINTR:
		return errInterrupted
	case -C.EPIPE:
		return errXrun
	}
	if code >= 0 {
		return nil
	}
	return fmt.Errorf("alsa: %s: %s (%d)", op, C.GoString(C.snd_strerror(code)), int(code))
}

func openNative(ctx context.Context, o Output, f audio.Format) (pcm, negotiated, error) {
	return openNativeConfigured(ctx, o, f, "")
}

// A private configuration lets ABI tests use an unoverrideable discard-only PCM
// instead of resolving "null" through the host's potentially customized config.
// Public Output always uses the normal system configuration (empty configText).
func openNativeConfigured(ctx context.Context, o Output, f audio.Format, configText string) (pcm, negotiated, error) {
	if err := ctx.Err(); err != nil {
		return nil, negotiated{}, err
	}
	name := C.CString(o.Device)
	defer C.free(unsafe.Pointer(name))
	var handle *C.snd_pcm_t
	mode := C.int(C.SND_PCM_NONBLOCK | C.SND_PCM_NO_AUTO_RESAMPLE | C.SND_PCM_NO_AUTO_CHANNELS | C.SND_PCM_NO_AUTO_FORMAT)
	var code C.int
	if configText == "" {
		code = C.snd_pcm_open(&handle, name, C.SND_PCM_STREAM_PLAYBACK, mode)
	} else {
		text := C.CString(configText)
		defer C.free(unsafe.Pointer(text))
		var config *C.snd_config_t
		if code = C.snd_config_load_string(&config, text, C.size_t(len(configText))); code < 0 {
			return nil, negotiated{}, alsaError("private-config", code)
		}
		defer C.snd_config_delete(config)
		code = C.snd_pcm_open_lconf(&handle, name, C.SND_PCM_STREAM_PLAYBACK, mode, config)
	}
	if code < 0 {
		return nil, negotiated{}, alsaError("open", code)
	}
	p := &nativePCM{handle: handle}
	var native C.sendspin_alsa_config
	code = C.sendspin_alsa_configure(handle, C.uint(f.SampleRate), C.uint(f.Channels), C.uint(o.BufferDuration.Microseconds()), C.uint(o.PeriodDuration.Microseconds()), &native)
	if code < 0 {
		_ = p.close()
		return nil, negotiated{}, alsaError("configure", code)
	}
	encoding := sampleEncoding(native.encoding)
	if uint64(native.buffer_frames) > uint64(f.SampleRate)*2 || uint64(native.period_frames) > uint64(f.SampleRate)*2 {
		_ = p.close()
		return nil, negotiated{}, ErrTiming
	}
	info := Info{DriverType: C.GoString(C.snd_pcm_type_name(C.snd_pcm_type(handle))), BufferFrames: int(native.buffer_frames), PeriodFrames: int(native.period_frames)}
	switch encoding {
	case encodingFloat:
		info.SampleFormat = "FLOAT_LE"
	case encodingS32:
		info.SampleFormat = "S32_LE"
	case encodingS24:
		info.SampleFormat = "S24_LE"
	case encodingS24Packed:
		info.SampleFormat = "S24_3LE"
	case encodingS16:
		info.SampleFormat = "S16_LE"
	}
	return p, negotiated{info: info, encoding: encoding, hardware: native.hardware != 0}, nil
}

func (p *nativePCM) snapshot() (nativeSnapshot, error) {
	var status C.sendspin_alsa_status
	if code := C.sendspin_alsa_snapshot(p.handle, &status); code < 0 {
		return nativeSnapshot{}, alsaError("status", code)
	}
	s := nativeSnapshot{delay: int64(status.delay), stamp: int64(status.stamp_ns), now: int64(status.now_ns)}
	switch status.state {
	case C.SND_PCM_STATE_SETUP:
		s.state = stateSetup
	case C.SND_PCM_STATE_PREPARED:
		s.state = statePrepared
	case C.SND_PCM_STATE_RUNNING:
		s.state = stateRunning
	case C.SND_PCM_STATE_XRUN:
		s.state = stateXrun
	case C.SND_PCM_STATE_SUSPENDED:
		s.state = stateSuspended
	}
	return s, nil
}
func (p *nativePCM) write(data []byte, frames int) (int, error) {
	if frames <= 0 || len(data) == 0 {
		return 0, nil
	}
	n := C.snd_pcm_writei(p.handle, unsafe.Pointer(&data[0]), C.snd_pcm_uframes_t(frames))
	if n < 0 {
		return 0, alsaError("write", C.int(n))
	}
	return int(n), nil
}
func (p *nativePCM) prepare() error { return alsaError("prepare", C.snd_pcm_prepare(p.handle)) }
func (p *nativePCM) start() error   { return alsaError("start", C.snd_pcm_start(p.handle)) }
func (p *nativePCM) drop() error    { return alsaError("drop", C.snd_pcm_drop(p.handle)) }
func (p *nativePCM) close() error   { return alsaError("close", C.snd_pcm_close(p.handle)) }
