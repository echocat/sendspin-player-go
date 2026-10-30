//go:build e2e && linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/audiotest"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	"github.com/echocat/slf4g/testing/recording"
)

// One software-only server per package, including -count repetitions. There are
// no hardware/session-manager modules and no connection to the host's server.
func TestE2EPrivatePipeWire(t *testing.T) {
	privateFixture.Do(func() {
		privateFixture.value, privateFixture.skip, privateFixture.err = startPrivatePipeWire(packageSuite.Directory())
	})
	if privateFixture.skip != "" {
		t.Skip(privateFixture.skip)
	}
	if privateFixture.err != nil {
		t.Fatal(privateFixture.err)
	}
	f := privateFixture.value
	for key, value := range f.environment() {
		t.Setenv(key, value)
	}
	t.Cleanup(func() {
		if err := f.graph.failure(); err != nil {
			t.Error("private native graph:", err)
		}
		if t.Failed() {
			t.Log("private PipeWire daemon log tail:", f.logTail())
			t.Log("private PipeWire registry:", f.graph.snapshot())
		}
	})
	t.Run("PlaybackLifecycle", func(t *testing.T) { testPrivatePlaybackLifecycle(t) })
	t.Run("ReferenceScenario", func(t *testing.T) { testPrivateReferenceScenario(t) })
	// Last in every repetition, without killing/restarting the shared server.
	t.Run("ServerLoss", func(t *testing.T) { testPrivateStreamLoss(t, f) })
}

func privateSilence(ctx context.Context, d audio.Device, samples []float32) error {
	// Only lifecycle silence may be explicitly discarded/rescheduled. The
	// reference scenario retains its strict health/timeline assertions.
	for range 8 {
		if _, err := d.Next(ctx); err != nil {
			return err
		}
		err := d.Write(ctx, samples)
		if !errors.Is(err, render.ErrClockReset) {
			return err
		}
		var reset interface{ AudioTimelineReset() bool }
		if errors.As(err, &reset) && !reset.AudioTimelineReset() {
			continue
		}
		if err := d.Flush(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("private fixture repeatedly missed its output reservation: %+v", d.(*render.Device).Diagnostics())
}

func testPrivatePlaybackLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(packageSuite.Context(), 30*time.Second)
	defer cancel()
	for cycle := range 3 {
		d, err := (Output{Device: "sendspin-null", Logger: recording.NewLogger()}).Open(ctx, audio.Format{SampleRate: 48000, Channels: 2})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		for range 10 {
			if err = privateSilence(ctx, d, make([]float32, 4800)); err != nil {
				t.Fatal("private lifecycle", cycle, err, d.(*render.Device).Diagnostics())
			}
		}
		if err = d.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		if err = privateSilence(ctx, d, make([]float32, 4800)); err != nil {
			t.Fatal(err)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
		if err = d.Close(); err != nil {
			t.Fatal("repeated close:", err)
		}
	}
	t.Log("three real native playback/flush/reopen cycles; private software sink, no hardware measurement")
}

func testPrivateReferenceScenario(t *testing.T) {
	// The shared helper generates/analyses WAVs through internal/loopback.Run,
	// and reuses the Suite's one worker for isolated logical reference peers.
	t.Setenv("SENDSPIN_TEST_PLAYBACK_DEVICE", "sendspin-null")
	t.Setenv("SENDSPIN_TEST_AUDIO_BACKEND", "pipewire")
	audiotest.RunReferenceServerPlaybackScenario(t, audiotest.ScenarioConfig{
		Suite: packageSuite, Prefix: "SENDSPIN_PIPEWIRE", SoftwareFixture: true,
		OutputFactory: func(device string) audio.Output { return privateDiagnosticOutput{Output: Output{Device: device}, t: t} },
	})
	t.Log("independent encrypted server -> SDK PCM/FLAC -> private native PipeWire graph; no physical conformance claim")
}

func testPrivateStreamLoss(t *testing.T, f *privatePipeWire) {
	ctx, cancel := context.WithTimeout(packageSuite.Context(), 10*time.Second)
	defer cancel()
	var native *driver
	o := Output{Device: "sendspin-null", Logger: recording.NewLogger()}
	d, err := render.Open(ctx, render.Config{Backend: "private PipeWire", Logger: o.Logger}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *render.Device) (render.Driver, error) {
		p, err := openNative(o, d)
		if err == nil {
			native = p.(*driver)
		}
		return p, err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err = privateSilence(ctx, d, make([]float32, 4800)); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() { written <- d.Write(ctx, make([]float32, d.(*render.Device).MaxSamples())) }()
	joinWriter := func() {
		cancel()
		// This is failure-path cleanup only. Report the joined writer's error
		// without replacing the primary assertion failure below.
		if cleanupErr := <-written; cleanupErr != nil {
			t.Log("writer cleanup:", cleanupErr)
		}
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	// Observe real native backpressure instead of assuming Sleep means blocked.
	for {
		select {
		case err := <-written:
			t.Fatal("writer returned before connection loss:", err)
		case <-deadline.C:
			joinWriter()
			t.Fatal("writer never reached native backpressure")
		case <-tick.C:
		}
		diag := d.(*render.Device).Diagnostics()
		if diag.QueuedFrames == diag.CapacityFrames {
			break
		}
	}
	// Same native state callback as server disconnection, but NOT evidence of
	// daemon-crash handling or absent-server fallback. Killing the shared daemon
	// would invalidate -count repetitions; never hide that with daemon restarts.
	native.lockLoop()
	code := f.graph.streamDisconnect(native.stream)
	native.unlockLoop()
	if code < 0 {
		joinWriter()
		t.Fatal(pwError("test stream disconnect", code))
	}
	select {
	case err = <-written:
		if !errors.Is(err, render.ErrDriver) {
			t.Fatal("native connection loss did not invalidate blocked writer:", err)
		}
	case <-time.After(2 * time.Second):
		joinWriter()
		t.Fatal("native connection loss did not wake blocked writer")
	}
	// Separate counter-evidence: an unavailable explicitly named private endpoint
	// must fail, never reconnect to the still-running private daemon or the host.
	if err := ctx.Err(); err != nil {
		t.Fatal("loss test context expired before absent-endpoint assertion:", err)
	}
	t.Setenv("PIPEWIRE_REMOTE", "sendspin-missing")
	if unexpected, err := o.Open(ctx, audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		_ = unexpected.Close()
		t.Fatal("unavailable explicit private endpoint opened; no fallback is allowed")
	} else if ctx.Err() != nil {
		t.Fatal("absent-endpoint failure was only context cancellation:", err)
	}
	t.Log("native stream-disconnect wakes blocked writer; unavailable explicit endpoint fails without fallback; daemon crash is NOT covered by this repeatable shared-daemon case")
}

type privateDiagnosticOutput struct {
	Output
	t *testing.T
}

type privateDiagnosticDevice struct {
	audio.Device
	t    *testing.T
	once sync.Once
	err  error
}

func (o privateDiagnosticOutput) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	d, err := o.Output.Open(ctx, f)
	if err != nil {
		return nil, err
	}
	return &privateDiagnosticDevice{Device: d, t: o.t}, nil
}

// Keep tail diagnostics without converting timeline/native failures into passes.
func (d *privateDiagnosticDevice) report(operation string, err error) error {
	if err != nil && !errors.Is(err, context.Canceled) {
		d.t.Logf("private PipeWire %s failed: %v; render diagnostics: %+v", operation, err, d.Device.(*render.Device).Diagnostics())
	}
	return err
}

func (d *privateDiagnosticDevice) Next(ctx context.Context) (time.Time, error) {
	at, err := d.Device.Next(ctx)
	return at, d.report("Next", err)
}

func (d *privateDiagnosticDevice) Write(ctx context.Context, samples []float32) error {
	return d.report("Write", d.Device.Write(ctx, samples))
}

func (d *privateDiagnosticDevice) Flush(ctx context.Context) error {
	return d.report("Flush", d.Device.Flush(ctx))
}

func (d *privateDiagnosticDevice) Close() error {
	d.once.Do(func() { d.err = d.report("Close", d.Device.Close()) })
	return d.err
}
