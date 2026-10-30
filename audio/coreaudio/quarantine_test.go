//go:build darwin && (amd64 || arm64)

package coreaudio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
)

// Both directions deliberately share the same fake API. After quarantine the
// opposite direction must fail before even querying its different device scope.
func tryOpenFakeDirection(f *fakeCoreAudio, input bool) (interface{ Close() error }, *driver, error) {
	format := audio.Format{SampleRate: 48000, Channels: 2}
	var p *driver
	if input {
		d, err := capture.Open(context.Background(), capture.Config{}, format, func(d *capture.Device) (capture.Driver, error) {
			v, err := openNativeInput(Input{}, d, &f.a)
			if err == nil {
				p = &v.(*inputDriver).driver
			}
			return v, err
		})
		return d, p, err
	}
	d, err := render.Open(context.Background(), render.Config{Backend: "coreaudio-test"}, format, func(d *render.Device) (render.Driver, error) {
		v, err := openNativeWithAPI(Output{}, d, &f.a)
		if err == nil {
			p = v.(*driver)
		}
		return v, err
	})
	return d, p, err
}

// Fake units cannot call us after their test ends; unlike production we can
// explicitly release intentionally quarantined state without a process restart.
func releaseFakeQuarantinedToken(token uintptr) {
	if p, ok := bridge.Value(token).(*inputDriver); ok {
		p.pins.Unpin()
	}
	bridge.Free(token)
}

func assertQuarantineRejectsReconnects(t *testing.T, f *fakeCoreAudio) {
	t.Helper()
	creations, queries, properties, token := f.creations, f.propertyQueries, len(f.properties), f.callback.Token
	state := bridge.Value(token)
	for i := range 32 {
		_, _, err := tryOpenFakeDirection(f, i%2 == 0)
		if !errors.Is(err, errQuarantined) {
			t.Fatalf("reconnect %d did not report sticky native quarantine: %v", i, err)
		}
	}
	if f.creations != creations || f.propertyQueries != queries || len(f.properties) != properties || f.callback.Token != token || bridge.Value(token) != state {
		t.Fatal("reconnect allocated new native units/tokens or accessed a quarantined API")
	}
}

func TestNativeDisposeFailureQuarantinesBothDirections(t *testing.T) {
	for _, input := range []bool{false, true} {
		name := "output"
		if input {
			name = "input"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeCoreAudio(t, input)
			d, p, err := tryOpenFakeDirection(f, input)
			if err != nil {
				t.Fatal(err)
			}
			defer releaseFakeQuarantinedToken(p.token)
			f.disposeStatus = -1
			if err := d.Close(); !errors.Is(err, errQuarantined) {
				t.Fatalf("unsafe Close: %v", err)
			}
			if p.released || bridge.Value(p.token) == nil || !p.closing.Load() {
				t.Fatal("quarantine freed unsafe callback state")
			}
			if input && len(bridge.Value(p.token).(*inputDriver).samples) == 0 {
				t.Fatal("quarantine released pinned capture storage")
			}
			assertQuarantineRejectsReconnects(t, f)
			// Native recovery after the fact cannot prove the retained unit safe.
			f.disposeStatus = 0
			assertQuarantineRejectsReconnects(t, f)
			if f.creations != 1 || f.disposals != 1 {
				t.Fatal("reconnect exceeded the one failed-unit/token budget")
			}
		})
	}
}

func TestPartialOpenDisposeFailureQuarantinesBothDirections(t *testing.T) {
	for _, input := range []bool{false, true} {
		name := "output"
		if input {
			name = "input"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeCoreAudio(t, input)
			f.initializeStatus, f.disposeStatus = -1, -2
			_, _, err := tryOpenFakeDirection(f, input)
			defer releaseFakeQuarantinedToken(f.callback.Token)
			if !errors.Is(err, errQuarantined) || bridge.Value(f.callback.Token) == nil {
				t.Fatalf("failed Open did not quarantine retained state: %v", err)
			}
			assertQuarantineRejectsReconnects(t, f)
			if f.creations != 1 || f.disposals != 1 {
				t.Fatal("failed Open retries leaked units")
			}
		})
	}
}

func TestJoinedDisposedStopFailureDoesNotQuarantine(t *testing.T) {
	for _, input := range []bool{false, true} {
		name := "output"
		if input {
			name = "input"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeCoreAudio(t, input)
			for range 4 {
				d, p, err := tryOpenFakeDirection(f, input)
				if err != nil {
					t.Fatal(err)
				}
				p.started = true
				f.stopStatus = -1
				if err := d.Close(); err == nil || errors.Is(err, errQuarantined) {
					t.Fatalf("cleanly disposed Stop failure: %v", err)
				}
				if !p.released || bridge.Value(p.token) != nil {
					t.Fatal("clean disposal retained callback token")
				}
			}
			if f.creations != 4 || f.disposals != 4 || f.a.quarantineErr != nil {
				t.Fatal("safe teardown blocked reconnect")
			}
		})
	}
}

func TestUnjoinedCallbackQuarantinesAndSerializesConcurrentOpen(t *testing.T) {
	for _, input := range []bool{false, true} {
		name := "output"
		if input {
			name = "input"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeCoreAudio(t, input)
			d, p, err := tryOpenFakeDirection(f, input)
			if err != nil {
				t.Fatal(err)
			}
			defer releaseFakeQuarantinedToken(p.token)
			defer func() {
				if err := d.Close(); !errors.Is(err, errQuarantined) {
					t.Errorf("device Close lost native quarantine: %v", err)
				}
			}()
			// Simulate a callback that cannot be joined. Calling native Close
			// directly avoids a separate Stop timeout and exercises its real
			// three-second safety deadline without changing production timing.
			p.active.Store(1)
			defer p.active.Store(0)
			closing := make(chan error, 1)
			go func() { closing <- p.Close() }()
			deadline := time.Now().Add(time.Second)
			for !p.closing.Load() {
				if time.Now().After(deadline) {
					t.Fatal("Close did not begin")
				}
				time.Sleep(time.Millisecond)
			}
			const attempts = 8
			errorsFromOpen := make(chan error, attempts)
			var opens sync.WaitGroup
			for range attempts {
				opens.Add(1)
				go func() { defer opens.Done(); _, _, err := tryOpenFakeDirection(f, input); errorsFromOpen <- err }()
			}
			if err := <-closing; !errors.Is(err, errQuarantined) {
				t.Fatalf("unjoined Close: %v", err)
			}
			opens.Wait()
			close(errorsFromOpen)
			for err := range errorsFromOpen {
				if !errors.Is(err, errQuarantined) {
					t.Fatalf("concurrent Open escaped quarantine: %v", err)
				}
			}
			if f.creations != 1 || f.disposals != 0 || p.released || bridge.Value(p.token) == nil {
				t.Fatal("unjoined callback allowed further allocation or unsafe disposal")
			}
			assertQuarantineRejectsReconnects(t, f)
		})
	}
}
