//go:build linux && (amd64 || arm64)

package native

import (
	"context"
	"errors"
	"testing"
)

func TestAutomaticLinuxPreferenceAndStrictOverridesWithoutDevices(t *testing.T) {
	missingPW := errors.New("pipewire unavailable")
	missingALSA := errors.New("alsa unavailable")
	for _, c := range []struct {
		name           string
		pwErr, alsaErr error
		strict         bool
		backend        Backend
		calls          int
	}{
		{"both present", nil, nil, false, BackendPipeWire, 0},
		{"alsa only", missingPW, nil, false, BackendALSA, 1},
		{"neither present", missingPW, missingALSA, false, BackendAuto, 1},
		{"explicit path fails", missingPW, nil, true, BackendPipeWire, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			value, backend, err := autoSelect(context.Background(), c.strict, func() (string, error) { return "pw", c.pwErr }, func() (string, error) { calls++; return "alsa", c.alsaErr })
			if backend != c.backend || calls != c.calls {
				t.Fatal("wrong backend order", backend, calls)
			}
			if c.pwErr == nil && value != "pw" {
				t.Fatal("PipeWire not preferred")
			}
			if c.pwErr != nil && c.alsaErr == nil && !c.strict && value != "alsa" {
				t.Fatal("available ALSA not selected")
			}
			if c.strict && !errors.Is(err, missingPW) {
				t.Fatal("explicit error lost", err)
			}
			if c.alsaErr != nil && (!errors.Is(err, missingPW) || !errors.Is(err, missingALSA)) {
				t.Fatal("candidate diagnostics lost", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, _, err := autoSelect(ctx, false, func() (int, error) { called = true; return 1, nil }, func() (int, error) { called = true; return 2, nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled detection touched a backend", err)
	}
}
