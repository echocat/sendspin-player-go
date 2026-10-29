//go:build darwin && (amd64 || arm64)

package coreaudio

import (
	"context"
	"errors"
	"math"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
)

// These tests bind only a purego callback trampoline. Every AudioUnit and
// AudioObject operation is fake: no device is opened, started or recorded.
type fakeProperty struct {
	id, scope, bus uint32
	value          uint32
}

type fakeCoreAudio struct {
	a                                           api
	scope, defaultSelector                      uint32
	properties                                  []fakeProperty
	format                                      streamDescription
	callback                                    callbackSpec
	starts, stops, disposals, renders           int
	creations, propertyQueries                  int
	rate                                        float64
	latency, streamLatency                      uint32
	disposeStatus, stopStatus, initializeStatus int32
	start                                       func() int32
	render                                      func(*uint32, *audioTimestamp, uint32, *bufferList) int32
}

func newFakeCoreAudio(t *testing.T, input bool) *fakeCoreAudio {
	t.Helper()
	f := &fakeCoreAudio{scope: scopeOutput, defaultSelector: 0x644f7574, rate: 48000, latency: 48, streamLatency: 24}
	if input {
		f.scope, f.defaultSelector = scopeInput, 0x64496e20
	}
	f.a.get = func(object uint32, addr *address, _ uint32, _ unsafe.Pointer, size *uint32, dst unsafe.Pointer) int32 {
		f.propertyQueries++
		switch addr.Selector {
		case f.defaultSelector:
			if object != 1 || addr.Scope != scopeGlobal {
				t.Error("wrong default device query")
			}
			*(*uint32)(dst) = 7
		case 0x75696464:
			tr := (*translation)(dst)
			if tr.InputBytes != 8 || tr.OutputBytes != 4 {
				t.Error("invalid UID translation")
			}
			*(*uint32)(tr.Output) = 7
		case 0x6e737274:
			*(*float64)(dst) = f.rate
		case 0x6c746e63:
			if object == 8 {
				if addr.Scope != scopeGlobal {
					t.Error("stream latency requires global scope")
				}
				*(*uint32)(dst) = f.streamLatency
			} else {
				if addr.Scope != f.scope {
					t.Error("queried latency of wrong direction")
				}
				*(*uint32)(dst) = f.latency
			}
		case 0x73746d23:
			if addr.Scope != f.scope {
				t.Error("queried streams of wrong direction")
			}
			*(*uint32)(dst) = 8
		case 0x6c69766e:
			*(*uint32)(dst) = 1
		default:
			t.Errorf("unexpected AudioObject property %#x", addr.Selector)
			return -50
		}
		return 0
	}
	f.a.getSize = func(_ uint32, addr *address, _ uint32, _ unsafe.Pointer, size *uint32) int32 {
		if addr.Selector != 0x73746d23 || addr.Scope != f.scope {
			t.Error("unexpected size query")
		}
		*size = 4
		return 0
	}
	f.a.nanos = func(host uint64) uint64 { return host }
	f.a.find = func(_ uintptr, desc *componentDescription) uintptr {
		if *desc != (componentDescription{Type: 0x61756f75, SubType: 0x6168616c, Manufacturer: 0x6170706c}) {
			t.Error("not Apple AUHAL")
		}
		return 11
	}
	f.a.newUnit = func(_ uintptr, unit *uintptr) int32 { f.creations++; *unit = 12; return 0 }
	f.a.set = func(_ uintptr, id, scope, bus uint32, value unsafe.Pointer, size uint32) int32 {
		p := fakeProperty{id: id, scope: scope, bus: bus}
		switch id {
		case propertyEnableIO, propertyCurrentDevice, propertyMaximumFrames:
			if size != 4 {
				t.Error("invalid UInt32 property size")
			}
			p.value = *(*uint32)(value)
		case propertyStreamFormat:
			if size != 40 {
				t.Error("invalid ASBD size")
			}
			f.format = *(*streamDescription)(value)
		case propertyInputCallback, propertyRenderCallback:
			if size != 16 {
				t.Error("invalid callback size")
			}
			f.callback = *(*callbackSpec)(value)
		default:
			t.Errorf("unexpected unit property %d", id)
		}
		f.properties = append(f.properties, p)
		return 0
	}
	f.a.getUnit = func(_ uintptr, id, scope, bus uint32, value unsafe.Pointer, size *uint32) int32 {
		wantScope, wantBus := uint32(auInput), uint32(0)
		if input {
			wantScope, wantBus = auOutput, 1
		}
		if id != propertyStreamFormat || scope != wantScope || bus != wantBus || *size != 40 {
			t.Error("wrong format readback")
		}
		*(*streamDescription)(value) = f.format
		return 0
	}
	f.a.initialize = func(uintptr) int32 { return f.initializeStatus }
	f.a.uninitialize = func(uintptr) int32 { return 0 }
	f.a.dispose = func(uintptr) int32 { f.disposals++; return f.disposeStatus }
	f.a.start = func(uintptr) int32 {
		f.starts++
		if f.start != nil {
			return f.start()
		}
		return 0
	}
	f.a.stop = func(uintptr) int32 { f.stops++; return f.stopStatus }
	f.a.reset = func(_ uintptr, scope, bus uint32) int32 {
		if scope != auGlobal || bus != 0 {
			t.Error("wrong reset scope")
		}
		return 0
	}
	f.a.stringNew = func(_ uintptr, s string, encoding uint32) uintptr {
		if s != "test-uid" || encoding != 0x08000100 {
			t.Error("invalid CF UID string")
		}
		return 99
	}
	f.a.release = func(v uintptr) {
		if v != 99 {
			t.Error("wrong CF release")
		}
	}
	f.a.render = func(_ uintptr, flags *uint32, stamp *audioTimestamp, bus, frames uint32, buffers *bufferList) int32 {
		f.renders++
		if bus != 1 {
			t.Error("capture must render bus 1")
		}
		if f.render != nil {
			return f.render(flags, stamp, frames, buffers)
		}
		return 0
	}
	return f
}

func openFakeInput(t *testing.T, f *fakeCoreAudio, uid string) (audio.InputDevice, *inputDriver) {
	t.Helper()
	var p *inputDriver
	d, err := capture.Open(context.Background(), capture.Config{Backend: "coreaudio-test", MaxTimingWindow: 50 * time.Millisecond}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *capture.Device) (capture.Driver, error) {
		v, err := openNativeInput(Input{Device: uid}, d, &f.a)
		if err == nil {
			p = v.(*inputDriver)
		}
		return v, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, p
}

func closeFakeDevice(t *testing.T, d interface{ Close() error }) {
	t.Helper()
	if err := d.Close(); err != nil {
		t.Errorf("close fake device: %v", err)
	}
}

func TestInputAUHALConfigurationAndNativeCallback(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, p := openFakeInput(t, f, "test-uid")
	defer closeFakeDevice(t, d)
	if f.starts != 0 {
		t.Fatal("Open started capture")
	}
	wantProperties := []fakeProperty{
		{propertyEnableIO, auInput, 1, 1}, {propertyEnableIO, auOutput, 0, 0},
		{propertyCurrentDevice, auGlobal, 0, 7}, {propertyStreamFormat, auOutput, 1, 0},
		{propertyMaximumFrames, auGlobal, 0, 24000}, {propertyInputCallback, auGlobal, 0, 0},
	}
	if !reflect.DeepEqual(f.properties, wantProperties) {
		t.Fatalf("properties: %+v", f.properties)
	}
	if f.callback.Function != inputCallbackPointer() || f.callback.Token != p.token {
		t.Fatal("invalid callback registration")
	}
	if p.latency != 72 {
		t.Fatal("device and stream latency not summed")
	}
	var invoke func(uintptr, *uint32, *audioTimestamp, uint32, uint32, *bufferList) int32
	purego.RegisterFunc(&invoke, f.callback.Function)
	want := []float32{0.25, -0.75, 0.5, -0.125}
	f.render = func(_ *uint32, _ *audioTimestamp, frames uint32, buffers *bufferList) int32 {
		if frames != 2 || buffers.Count != 1 || buffers.First.Channels != 2 || buffers.First.Bytes != 16 {
			t.Error("invalid input storage")
		}
		copy(unsafe.Slice((*float32)(buffers.First.Data), 4), want)
		return 0
	}
	before := time.Now()
	f.start = func() int32 {
		flags := uint32(0)
		stamp := audioTimestamp{SampleTime: 128, HostTime: uint64(bridge.Now() - int64(3*time.Millisecond)), Flags: 3}
		return invoke(f.callback.Token, &flags, &stamp, 1, 2, nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := make([]float32, 4)
	n, first, err := d.Read(ctx, out)
	if err != nil || n != 4 || !reflect.DeepEqual(out, want) {
		t.Fatalf("read: %d %v %v", n, out, err)
	}
	if first.IsZero() || first.After(time.Now()) || first.Before(before.Add(-100*time.Millisecond)) {
		t.Fatalf("invalid local ADC time: %v", first)
	}
	if !reflect.DeepEqual(p.samples[:4], want) {
		t.Fatal("capture modified input amplitudes")
	}
	// Read retains Go's monotonic component, not a wall-clock reconstruction.
	// Equal ignores that component, so compare the complete time representation.
	if reflect.DeepEqual(first, first.Round(0)) {
		t.Fatal("capture timestamp lost monotonic clock")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if f.starts != 1 || f.stops != 1 || f.disposals != 1 || bridge.Value(f.callback.Token) != nil {
		t.Fatal("incorrect close lifecycle")
	}
	if err := d.Close(); err != nil || f.disposals != 1 {
		t.Fatal("Close is not idempotent")
	}
}

func TestOutputAUHALConfigurationAndClose(t *testing.T) {
	f := newFakeCoreAudio(t, false)
	var p *driver
	d, err := render.Open(context.Background(), render.Config{Backend: "coreaudio-test"}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *render.Device) (render.Driver, error) {
		v, err := openNativeWithAPI(Output{}, d, &f.a)
		if err == nil {
			p = v.(*driver)
		}
		return v, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFakeDevice(t, d)
	if f.starts != 0 || f.properties[0] != (fakeProperty{propertyEnableIO, auInput, 1, 0}) || f.properties[1] != (fakeProperty{propertyEnableIO, auOutput, 0, 1}) {
		t.Fatal("output did not disable input")
	}
	if f.properties[3].scope != auInput || f.properties[3].bus != 0 {
		t.Fatal("wrong output client format scope")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if bridge.Value(p.token) != nil || f.disposals != 1 {
		t.Fatal("output callback token leaked")
	}
}

func TestInputTimeSubtractsOnlyInputHardwareLatency(t *testing.T) {
	if got := inputTime(1_000_000_000, 72, 48000); got != 998_500_000 {
		t.Fatalf("ADC timestamp: %d", got)
	}
	for _, tc := range []struct {
		host    uint64
		latency uint32
		rate    float64
	}{
		{0, 0, 48000}, {1, 72, 48000}, {math.MaxUint64, 0, 48000}, {1_000_000_000, 0, 0},
		{1_000_000_000, 0, math.NaN()}, {1_000_000_000, 0, math.Inf(1)}, {1_000_000_000, 96001, 48000},
	} {
		if inputTime(tc.host, tc.latency, tc.rate) != 0 {
			t.Fatalf("accepted invalid timing: %+v", tc)
		}
	}
}

func TestInputCloseRetainsSafeTokenOnNativeFailure(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, p := openFakeInput(t, f, "")
	f.disposeStatus, f.stopStatus = -1, -2
	p.started = true
	if err := d.Close(); err == nil {
		t.Fatal("native disposal failure hidden")
	}
	if bridge.Value(p.token) != p || p.released || !p.closing.Load() {
		t.Fatal("failed disposal freed callback state")
	}
	flags := uint32(0)
	stamp := audioTimestamp{Flags: 3}
	if code := captureCallback(p.token, &flags, &stamp, 1, 2, nil); code != 0 || f.renders != 0 {
		t.Fatal("late callback rendered after failed close")
	}
	if err := d.Close(); err == nil || f.disposals != 1 {
		t.Fatal("failed Close must remain idempotent")
	}
	// The fake native unit cannot issue callbacks: explicitly release its leak.
	bridge.Free(p.token)
	p.pins.Unpin()
}

func TestInputCloseJoinsRenderBeforeDispose(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, p := openFakeInput(t, f, "")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.render = func(_ *uint32, _ *audioTimestamp, _ uint32, _ *bufferList) int32 {
		once.Do(func() { close(entered) })
		<-release
		return 0
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		flags := uint32(0)
		stamp := audioTimestamp{Flags: 3, HostTime: uint64(bridge.Now())}
		captureCallback(p.token, &flags, &stamp, 1, 2, nil)
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close did not join render: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	<-done
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if f.disposals != 1 || bridge.Value(p.token) != nil {
		t.Fatal("close failed to release joined callback")
	}
}

func TestInputRejectsRateChangesAndPropagatesRenderErrors(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, p := openFakeInput(t, f, "")
	defer closeFakeDevice(t, d)
	f.rate = 44100
	if !errors.Is(p.Check(), capture.ErrDriver) {
		t.Fatal("rate change not detected")
	}
	f.rate = 48000
	f.streamLatency++
	if !errors.Is(p.Check(), capture.ErrDriver) {
		t.Fatal("stream latency change not detected")
	}
	f.streamLatency--
	f.render = func(_ *uint32, _ *audioTimestamp, _ uint32, _ *bufferList) int32 { return -10874 }
	f.start = func() int32 {
		flags := uint32(0)
		stamp := audioTimestamp{Flags: 3, HostTime: uint64(bridge.Now())}
		return captureCallback(p.token, &flags, &stamp, 1, 2, nil)
	}
	if _, _, err := d.Read(context.Background(), make([]float32, 4)); err == nil {
		t.Fatal("native render error was hidden")
	}
}

func TestInputInitializationFailureDisposesAndFreesToken(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	f.initializeStatus = -1
	_, err := capture.Open(context.Background(), capture.Config{}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *capture.Device) (capture.Driver, error) { return openNativeInput(Input{}, d, &f.a) })
	if err == nil || f.disposals != 1 || bridge.Value(f.callback.Token) != nil {
		t.Fatal("failed initialization retained callback token")
	}
	if f.starts != 0 {
		t.Fatal("failed initialization started capture")
	}
}

func TestInputPartialStartFailureStillStopsNativeUnit(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, _ := openFakeInput(t, f, "")
	f.start = func() int32 { return -1 }
	if _, _, err := d.Read(context.Background(), make([]float32, 4)); err == nil {
		t.Fatal("native start error was hidden")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if f.stops != 1 || f.disposals != 1 {
		t.Fatal("partially started unit was not stopped and disposed")
	}
}

func TestInputZeroStartupTimestampReleasesNoGuessedData(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, p := openFakeInput(t, f, "")
	defer closeFakeDevice(t, d)
	f.render = func(_ *uint32, stamp *audioTimestamp, _ uint32, b *bufferList) int32 {
		value := float32(0.75)
		if stamp.HostTime == 0 {
			value = 0.25
		}
		samples := unsafe.Slice((*float32)(b.First.Data), 4)
		for i := range samples {
			samples[i] = value
		}
		return 0
	}
	f.start = func() int32 {
		flags := uint32(0)
		stamp := audioTimestamp{Flags: 3}
		if code := captureCallback(p.token, &flags, &stamp, 1, 2, nil); code != 0 {
			return code
		}
		stamp.SampleTime, stamp.HostTime = 2, uint64(bridge.Now())
		return captureCallback(p.token, &flags, &stamp, 1, 2, nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := make([]float32, 4)
	if n, first, err := d.Read(ctx, out); err != nil || n != 4 || first.IsZero() || !reflect.DeepEqual(out, []float32{0.75, 0.75, 0.75, 0.75}) {
		t.Fatalf("startup zero timestamp released guessed data: n=%d first=%v out=%v err=%v", n, first, out, err)
	}
}

func TestInputCanceledAndPartialReadsDoNotStartCapture(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, _ := openFakeInput(t, f, "")
	defer closeFakeDevice(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.Read(ctx, make([]float32, 4)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Read: %v", err)
	}
	if _, _, err := d.Read(context.Background(), make([]float32, 3)); err == nil {
		t.Fatal("accepted partial stereo frame")
	}
	if _, _, err := d.Read(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if f.starts != 0 {
		t.Fatal("canceled, partial or empty Read started capture")
	}
}

func TestInputRejectsDeviceRateMismatchWithoutChangingHardware(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	f.rate = 44100
	_, err := capture.Open(context.Background(), capture.Config{}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *capture.Device) (capture.Driver, error) { return openNativeInput(Input{}, d, &f.a) })
	if err == nil || len(f.properties) != 0 || f.starts != 0 {
		t.Fatal("AUHAL rate mismatch was accepted or changed device")
	}
}

func TestInputReportsSampleTimeDiscontinuity(t *testing.T) {
	f := newFakeCoreAudio(t, true)
	d, p := openFakeInput(t, f, "")
	defer closeFakeDevice(t, d)
	flags := uint32(0)
	stamp := audioTimestamp{SampleTime: 0, HostTime: uint64(bridge.Now()), Flags: 3}
	if captureCallback(p.token, &flags, &stamp, 1, 2, nil) != 0 {
		t.Fatal("first valid callback failed")
	}
	stamp.SampleTime = 10 // previous block ended at 2
	if captureCallback(p.token, &flags, &stamp, 1, 2, nil) == 0 {
		t.Fatal("native discontinuity was hidden")
	}
	_, _, err := d.Read(context.Background(), make([]float32, 4))
	var discontinuity interface{ AudioInputDiscontinuity() bool }
	if !errors.As(err, &discontinuity) || !discontinuity.AudioInputDiscontinuity() || !errors.Is(err, capture.ErrTiming) {
		t.Fatalf("wrong discontinuity error: %v", err)
	}
}

func TestInputRejectsMalformedNativeBuffer(t *testing.T) {
	for _, malformed := range []string{"count", "channels", "bytes", "pointer"} {
		t.Run(malformed, func(t *testing.T) {
			f := newFakeCoreAudio(t, true)
			d, p := openFakeInput(t, f, "")
			defer closeFakeDevice(t, d)
			f.render = func(_ *uint32, _ *audioTimestamp, _ uint32, b *bufferList) int32 {
				switch malformed {
				case "count":
					b.Count = 2
				case "channels":
					b.First.Channels = 1
				case "bytes":
					b.First.Bytes = 4
				case "pointer":
					b.First.Data = nil
				}
				return 0
			}
			flags := uint32(0)
			stamp := audioTimestamp{Flags: 3, HostTime: uint64(bridge.Now())}
			if captureCallback(p.token, &flags, &stamp, 1, 2, nil) == 0 {
				t.Fatal("accepted malformed native storage")
			}
			if _, _, err := d.Read(context.Background(), make([]float32, 4)); !errors.Is(err, capture.ErrDriver) {
				t.Fatalf("wrong malformed buffer error: %v", err)
			}
		})
	}
}

func TestOutputLateNativeCallbackSilencesAfterDisposeFailure(t *testing.T) {
	f := newFakeCoreAudio(t, false)
	var p *driver
	d, err := render.Open(context.Background(), render.Config{Backend: "coreaudio-test"}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *render.Device) (render.Driver, error) {
		v, err := openNativeWithAPI(Output{}, d, &f.a)
		if err == nil {
			p = v.(*driver)
		}
		return v, err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.disposeStatus = -1
	if d.Close() == nil || bridge.Value(p.token) != p {
		t.Fatal("output failure freed token")
	}
	var invoke func(uintptr, *uint32, *audioTimestamp, uint32, uint32, *bufferList) int32
	purego.RegisterFunc(&invoke, f.callback.Function)
	samples := []float32{1, -1, 0.5, -0.5}
	buffers := bufferList{Count: 1, First: audioBuffer{Channels: 2, Bytes: 16, Data: unsafe.Pointer(&samples[0])}}
	var pins runtime.Pinner
	pins.Pin(&samples[0])
	pins.Pin(&buffers)
	defer pins.Unpin()
	flags := uint32(0)
	stamp := audioTimestamp{Flags: 2, HostTime: uint64(bridge.Now())}
	if invoke(p.token, &flags, &stamp, 0, 2, &buffers) != 0 {
		t.Fatal("late output callback failed")
	}
	if !reflect.DeepEqual(samples, []float32{0, 0, 0, 0}) {
		t.Fatal("late output callback did not silence buffer")
	}
	bridge.Free(p.token)
}
