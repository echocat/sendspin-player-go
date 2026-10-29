package native

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestExplicitLibraryCannotFallBackToAnotherBackend(t *testing.T) {
	if _, err := (Output{SharedLibPath: filepath.Join(t.TempDir(), "absent-explicit-library")}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		t.Fatal("explicit library override did not fail")
	}
}
func TestNoopMustBeExplicitAndNeverCreatesInput(t *testing.T) {
	d, err := (Output{Backend: BackendNoop}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if d.(SelectedBackend).Backend() != BackendNoop {
		t.Fatal("noop selection lost")
	}
	if _, err = (Input{Backend: BackendNoop}).Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		t.Fatal("noop silently became a capture source")
	}
}
