package oto

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func floatBytes(samples ...float32) []byte {
	buf := make([]byte, len(samples)*4)
	for i, sample := range samples {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(sample))
	}
	return buf
}

func TestSourceWrapSilenceAndOwnership(t *testing.T) {
	s := newSource(audio.Format{SampleRate: 8000, Channels: 1})
	s.ring = make([]byte, 16)
	samples := []float32{1, 2, 3}
	if err := s.write(context.Background(), samples, nil); err != nil {
		t.Fatal(err)
	}
	clear(samples)
	first := make([]byte, 7) // Deliberately split a float across reader calls.
	if n, err := s.Read(first); n != 7 || err != nil {
		t.Fatal(n, err)
	}
	if err := s.write(context.Background(), []float32{4, 5}, nil); err != nil {
		t.Fatal(err)
	}
	rest := make([]byte, 17)
	if n, err := s.Read(rest); n != 17 || err != nil {
		t.Fatal(n, err)
	}
	got := append(first, rest...)
	want := append(floatBytes(1, 2, 3, 4, 5), make([]byte, 4)...)
	if string(got) != string(want) || s.buffered() != 0 {
		t.Fatal("ring replay, ownership or starvation silence differs")
	}
	if _, err := s.Seek(1, io.SeekStart); err == nil {
		t.Fatal("accepted arbitrary seek on live PCM source")
	}
	if err := s.write(context.Background(), []float32{6}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Seek(0, io.SeekStart); err != nil || s.buffered() != 0 {
		t.Fatal("reset retained old data", err)
	}
	s.close()
	if _, err := s.Read(rest); !errors.Is(err, io.EOF) {
		t.Fatal("closed reader remained active", err)
	}
	if err := s.write(context.Background(), []float32{1}, nil); err == nil {
		t.Fatal("write after close")
	}
}

func TestSourceBackpressureCancelsAndReportsBackendFailure(t *testing.T) {
	for _, backendFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation", true: "backend-error"}[backendFailure], func(t *testing.T) {
			s := newSource(audio.Format{SampleRate: 8000, Channels: 2})
			s.ring = make([]byte, 8)
			if err := s.write(context.Background(), []float32{1, 2}, nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			entered := make(chan struct{})
			failure := errors.New("backend stopped")
			calls := 0
			status := func() error {
				calls++
				if calls == 1 {
					close(entered)
					return nil
				}
				if backendFailure {
					return failure
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- s.write(ctx, []float32{3, 4}, status) }()
			<-entered
			if !backendFailure {
				cancel()
			}
			select {
			case err := <-done:
				if backendFailure && !errors.Is(err, failure) || !backendFailure && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-ctx.Done():
				if backendFailure {
					t.Fatal("failed backend pinned the writer")
				}
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			if s.buffered() != 8 {
				t.Fatal("backpressured write exceeded bounded queue")
			}
		})
	}
}

func TestSourceRejectsPartialFrames(t *testing.T) {
	s := newSource(audio.Format{SampleRate: 48000, Channels: 2})
	if err := s.write(context.Background(), []float32{1}, nil); err == nil {
		t.Fatal("accepted partial stereo frame")
	}
}
