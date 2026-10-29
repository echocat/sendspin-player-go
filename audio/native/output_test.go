package native_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/native"
)

func TestSelectionValidationAndCancellationAreInert(t *testing.T) {
	outputs := []audio.Output{native.Output{}, native.Output{Backend: native.BackendNoop}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, output := range outputs {
		if _, err := output.Open(ctx, audio.Format{SampleRate: 48000, Channels: 2}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := output.Open(context.Background(), audio.Format{}); err == nil {
			t.Fatal("invalid format reached native API")
		}
	}
}
