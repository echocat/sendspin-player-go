//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
)

// null is ALSA's discard-only software PCM, not a host device or audio server.
func TestNativeNullPCMConfigurationAndPluginPolicy(t *testing.T) {
	if _, err := loadALSA(""); err != nil {
		if os.Getenv("SENDSPIN_ALSA_REQUIRE_LIBRARY") == "1" {
			t.Fatal(err)
		}
		t.Skip("optional libasound runtime unavailable; fake tests still run:", err)
	}
	f := audio.Format{SampleRate: 48000, Channels: 2}
	open := func(ctx context.Context, o Output, f audio.Format) (pcm, negotiated, error) {
		return openNativeConfigured(ctx, o, f, "pcm.sendspin_test_null { type null }")
	}
	if _, err := openWith(context.Background(), Output{Device: "sendspin_test_null"}, f, open); !errors.Is(err, ErrPluginTiming) {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := openWith(ctx, Output{Device: "sendspin_test_null", AllowPluginTiming: true}, f, open)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	info := d.(*device).Info()
	if !info.Approximate || info.DriverType != "NULL" || info.SampleRate != 48000 || info.SampleFormat != "FLOAT_LE" {
		t.Fatal(info)
	}
	// null consumes without a physical clock. Test native ABI/lifecycle directly;
	// do not reinterpret its clock behavior as timing/conformance evidence.
	p := d.(*device).pcm
	if err = p.prepare(); err != nil {
		t.Fatal(err)
	}
	if n, err := p.write(make([]byte, 96*4), 48); err != nil || n != 48 {
		t.Fatal(n, err)
	}
	if err = p.start(); err != nil {
		t.Fatal(err)
	}
	if _, err = p.snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = d.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
}
