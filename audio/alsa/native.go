//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"fmt"
	"sync"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/dynlib"
	"golang.org/x/sys/unix"
)

type alsaAPI struct {
	strerror                                                    func(int32) string
	open                                                        func(*uintptr, string, int32, int32) int32
	openConfig                                                  func(*uintptr, string, int32, int32, uintptr) int32
	configLoad                                                  func(*uintptr, string, uint64) int32
	configDelete                                                func(uintptr) int32
	typeOf                                                      func(uintptr) int32
	typeName                                                    func(int32) string
	formatValue                                                 func(string) int32
	hwMalloc, swMalloc, statusMalloc                            func(*uintptr) int32
	hwFree, swFree, statusFree                                  func(uintptr)
	hwAny                                                       func(uintptr, uintptr) int32
	hwAccess, hwFormat, hwTestFormat                            func(uintptr, uintptr, int32) int32
	hwResample, hwChannels                                      func(uintptr, uintptr, uint32) int32
	hwRate                                                      func(uintptr, uintptr, uint32, int32) int32
	hwPeriodTime, hwBufferTime                                  func(uintptr, uintptr, *uint32, *int32) int32
	hwApply, swCurrent, swApply, status                         func(uintptr, uintptr) int32
	hwBuffer                                                    func(uintptr, *uint64) int32
	hwPeriod                                                    func(uintptr, *uint64, *int32) int32
	swBoundary                                                  func(uintptr, *uint64) int32
	swStart, swStop, swSilenceThreshold, swSilenceSize, swAvail func(uintptr, uintptr, uint64) int32
	swTimestamp, swTimestampType                                func(uintptr, uintptr, int32) int32
	prepare, start, drop, close                                 func(uintptr) int32
	statusStamp                                                 func(uintptr, *unix.Timespec)
	statusState                                                 func(uintptr) int32
	statusDelay                                                 func(uintptr) int64
	write                                                       func(uintptr, *byte, uint64) int64
	read                                                        func(uintptr, *byte, uint64) int64
}

var alsaLibraries = struct {
	sync.Mutex
	values map[string]*alsaAPI
}{values: make(map[string]*alsaAPI)}

func loadALSA(path string) (*alsaAPI, error) {
	lib, err := dynlib.Open(path, "libasound.so.2")
	if err != nil {
		return nil, err
	}
	alsaLibraries.Lock()
	defer alsaLibraries.Unlock()
	if a := alsaLibraries.values[lib.Path]; a != nil {
		return a, nil
	}
	a := &alsaAPI{}
	for _, b := range []struct {
		dst  any
		name string
	}{
		{&a.strerror, "snd_strerror"}, {&a.open, "snd_pcm_open"}, {&a.openConfig, "snd_pcm_open_lconf"}, {&a.configLoad, "snd_config_load_string"}, {&a.configDelete, "snd_config_delete"}, {&a.typeOf, "snd_pcm_type"}, {&a.typeName, "snd_pcm_type_name"}, {&a.formatValue, "snd_pcm_format_value"},
		{&a.hwMalloc, "snd_pcm_hw_params_malloc"}, {&a.hwFree, "snd_pcm_hw_params_free"}, {&a.swMalloc, "snd_pcm_sw_params_malloc"}, {&a.swFree, "snd_pcm_sw_params_free"}, {&a.statusMalloc, "snd_pcm_status_malloc"}, {&a.statusFree, "snd_pcm_status_free"},
		{&a.hwAny, "snd_pcm_hw_params_any"}, {&a.hwAccess, "snd_pcm_hw_params_set_access"}, {&a.hwResample, "snd_pcm_hw_params_set_rate_resample"}, {&a.hwChannels, "snd_pcm_hw_params_set_channels"}, {&a.hwRate, "snd_pcm_hw_params_set_rate"}, {&a.hwTestFormat, "snd_pcm_hw_params_test_format"}, {&a.hwFormat, "snd_pcm_hw_params_set_format"}, {&a.hwPeriodTime, "snd_pcm_hw_params_set_period_time_near"}, {&a.hwBufferTime, "snd_pcm_hw_params_set_buffer_time_near"}, {&a.hwApply, "snd_pcm_hw_params"}, {&a.hwBuffer, "snd_pcm_hw_params_get_buffer_size"}, {&a.hwPeriod, "snd_pcm_hw_params_get_period_size"},
		{&a.swCurrent, "snd_pcm_sw_params_current"}, {&a.swBoundary, "snd_pcm_sw_params_get_boundary"}, {&a.swStart, "snd_pcm_sw_params_set_start_threshold"}, {&a.swStop, "snd_pcm_sw_params_set_stop_threshold"}, {&a.swSilenceThreshold, "snd_pcm_sw_params_set_silence_threshold"}, {&a.swSilenceSize, "snd_pcm_sw_params_set_silence_size"}, {&a.swAvail, "snd_pcm_sw_params_set_avail_min"}, {&a.swTimestamp, "snd_pcm_sw_params_set_tstamp_mode"}, {&a.swTimestampType, "snd_pcm_sw_params_set_tstamp_type"}, {&a.swApply, "snd_pcm_sw_params"},
		{&a.prepare, "snd_pcm_prepare"}, {&a.start, "snd_pcm_start"}, {&a.drop, "snd_pcm_drop"}, {&a.close, "snd_pcm_close"}, {&a.status, "snd_pcm_status"}, {&a.statusStamp, "snd_pcm_status_get_htstamp"}, {&a.statusState, "snd_pcm_status_get_state"}, {&a.statusDelay, "snd_pcm_status_get_delay"}, {&a.write, "snd_pcm_writei"}, {&a.read, "snd_pcm_readi"},
	} {
		if err = lib.Bind(b.dst, b.name); err != nil {
			return nil, err
		}
	}
	alsaLibraries.values[lib.Path] = a
	return a, nil
}
func (a *alsaAPI) err(op string, code int32) error {
	switch code {
	case -int32(unix.EAGAIN):
		return errAgain
	case -int32(unix.EINTR):
		return errInterrupted
	case -int32(unix.EPIPE):
		return errXrun
	}
	if code >= 0 {
		return nil
	}
	return fmt.Errorf("alsa: %s: %s (%d)", op, a.strerror(code), code)
}

type nativePCM struct {
	handle, status uintptr
	api            *alsaAPI
}

func openNative(ctx context.Context, o Output, f audio.Format) (pcm, negotiated, error) {
	return openNativeConfigured(ctx, o, f, "")
}
func openNativeConfigured(ctx context.Context, o Output, f audio.Format, text string) (pcm, negotiated, error) {
	return openNativeDirection(ctx, o, f, text, 0)
}
func openNativeDirection(ctx context.Context, o Output, f audio.Format, text string, direction int32) (pcm, negotiated, error) {
	if err := ctx.Err(); err != nil {
		return nil, negotiated{}, err
	}
	a, err := loadALSA(o.SharedLibPath)
	if err != nil {
		return nil, negotiated{}, err
	}
	p := &nativePCM{api: a}
	const mode = 1 | 0x10000 | 0x20000 | 0x40000
	var code int32
	if text == "" {
		code = a.open(&p.handle, o.Device, direction, mode)
	} else {
		var config uintptr
		if code = a.configLoad(&config, text, uint64(len(text))); code < 0 {
			return nil, negotiated{}, a.err("private config", code)
		}
		code = a.openConfig(&p.handle, o.Device, direction, mode, config)
		_ = a.configDelete(config)
	}
	if code < 0 {
		return nil, negotiated{}, a.err("open", code)
	}
	fail := func(err error) (pcm, negotiated, error) { _ = p.close(); return nil, negotiated{}, err }
	info, encoding, err := p.configure(o, f)
	if err != nil {
		return fail(err)
	}
	if code = a.statusMalloc(&p.status); code < 0 {
		return fail(a.err("allocate status", code))
	}
	return p, negotiated{info: info, encoding: encoding, hardware: a.typeOf(p.handle) == 0}, nil
}
func (p *nativePCM) configure(o Output, f audio.Format) (Info, sampleEncoding, error) {
	a := p.api
	h := p.handle
	var hw, sw uintptr
	if code := a.hwMalloc(&hw); code < 0 {
		return Info{}, 0, a.err("allocate hardware params", code)
	}
	defer a.hwFree(hw)
	if code := a.swMalloc(&sw); code < 0 {
		return Info{}, 0, a.err("allocate software params", code)
	}
	defer a.swFree(sw)
	for _, call := range []func() int32{func() int32 { return a.hwAny(h, hw) }, func() int32 { return a.hwAccess(h, hw, 3) }, func() int32 { return a.hwResample(h, hw, 0) }, func() int32 { return a.hwChannels(h, hw, uint32(f.Channels)) }, func() int32 { return a.hwRate(h, hw, uint32(f.SampleRate), 0) }} {
		if code := call(); code < 0 {
			return Info{}, 0, a.err("configure hardware", code)
		}
	}
	var encoding sampleEncoding
	var name string
	selected := int32(-1)
	for i, n := range []string{"FLOAT_LE", "S32_LE", "S24_LE", "S24_3LE", "S16_LE"} {
		value := a.formatValue(n)
		if value >= 0 && a.hwTestFormat(h, hw, value) == 0 {
			selected = value
			encoding = sampleEncoding(i + 1)
			name = n
			break
		}
	}
	if selected < 0 {
		return Info{}, 0, fmt.Errorf("alsa: no supported sample encoding")
	}
	periodUS, bufferUS := uint32(o.PeriodDuration.Microseconds()), uint32(o.BufferDuration.Microseconds())
	var direction int32
	var buffer, period, boundary uint64
	for _, call := range []func() int32{
		func() int32 { return a.hwFormat(h, hw, selected) }, func() int32 { return a.hwPeriodTime(h, hw, &periodUS, &direction) }, func() int32 { direction = 0; return a.hwBufferTime(h, hw, &bufferUS, &direction) }, func() int32 { return a.hwApply(h, hw) }, func() int32 { return a.hwBuffer(hw, &buffer) }, func() int32 { direction = 0; return a.hwPeriod(hw, &period, &direction) }, func() int32 { return a.swCurrent(h, sw) }, func() int32 { return a.swBoundary(sw, &boundary) },
		func() int32 { return a.swStart(h, sw, boundary) }, func() int32 { return a.swStop(h, sw, buffer) }, func() int32 { return a.swSilenceThreshold(h, sw, 0) }, func() int32 { return a.swSilenceSize(h, sw, 0) }, func() int32 { return a.swAvail(h, sw, period) }, func() int32 { return a.swTimestamp(h, sw, 1) }, func() int32 { return a.swTimestampType(h, sw, 1) }, func() int32 { return a.swApply(h, sw) }, func() int32 { return a.prepare(h) },
	} {
		if code := call(); code < 0 {
			return Info{}, 0, a.err("configure", code)
		}
	}
	if buffer > uint64(f.SampleRate)*2 || period > uint64(f.SampleRate)*2 {
		return Info{}, 0, ErrTiming
	}
	return Info{DriverType: a.typeName(a.typeOf(h)), SampleFormat: name, BufferFrames: int(buffer), PeriodFrames: int(period)}, encoding, nil
}
func (p *nativePCM) snapshot() (nativeSnapshot, error) {
	if code := p.api.status(p.handle, p.status); code < 0 {
		return nativeSnapshot{}, p.api.err("status", code)
	}
	var stamp unix.Timespec
	p.api.statusStamp(p.status, &stamp)
	s := nativeSnapshot{delay: p.api.statusDelay(p.status), stamp: stamp.Nano(), now: bridge.Now()}
	switch p.api.statusState(p.status) {
	case 1:
		s.state = stateSetup
	case 2:
		s.state = statePrepared
	case 3:
		s.state = stateRunning
	case 4:
		s.state = stateXrun
	case 7:
		s.state = stateSuspended
	}
	return s, nil
}
func (p *nativePCM) write(data []byte, frames int) (int, error) {
	if frames <= 0 || len(data) == 0 {
		return 0, nil
	}
	n := p.api.write(p.handle, &data[0], uint64(frames))
	if n < 0 {
		return 0, p.api.err("write", int32(n))
	}
	return int(n), nil
}
func (p *nativePCM) prepare() error { return p.api.err("prepare", p.api.prepare(p.handle)) }
func (p *nativePCM) start() error   { return p.api.err("start", p.api.start(p.handle)) }
func (p *nativePCM) drop() error    { return p.api.err("drop", p.api.drop(p.handle)) }
func (p *nativePCM) close() error {
	if p.status != 0 {
		p.api.statusFree(p.status)
		p.status = 0
	}
	if p.handle == 0 {
		return nil
	}
	code := p.api.close(p.handle)
	p.handle = 0
	return p.api.err("close", code)
}
