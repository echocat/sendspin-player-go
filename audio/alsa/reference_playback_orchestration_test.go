package alsa

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func weakReferenceFixture(t *testing.T) string {
	t.Helper()
	b := make([]byte, 44+144000*4)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 2)
	binary.LittleEndian.PutUint32(b[24:], 48000)
	binary.LittleEndian.PutUint32(b[28:], 192000)
	binary.LittleEndian.PutUint16(b[32:], 4)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(b)-44))
	binary.LittleEndian.PutUint16(b[44+48000*4:], 320)
	path := filepath.Join(t.TempDir(), "synthetic-weak.wav")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

type referenceSink struct {
	frames, flushes int
	failure         error
}

func (s *referenceSink) Next(ctx context.Context) (time.Time, error) {
	return time.Time{}, errors.Join(ctx.Err(), s.failure)
}
func (s *referenceSink) Write(_ context.Context, v []float32) error {
	s.frames += len(v) / 2
	return nil
}
func (s *referenceSink) Flush(context.Context) error { s.flushes++; return nil }
func (s *referenceSink) Close() error                { return nil }

func TestWeakReferenceLoaderAndSDKSubmissionUseOnlyFakes(t *testing.T) {
	samples, err := loadWeakReference(weakReferenceFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	sink := &referenceSink{}
	frames, err := submitWeakReference(context.Background(), sink, samples)
	if err != nil || frames != 144000 || sink.frames != frames || sink.flushes != 1 {
		t.Fatal(frames, sink, err)
	}
	fault := errors.New("controlled timing failure")
	sink = &referenceSink{failure: fault}
	if frames, err = submitWeakReference(context.Background(), sink, samples); !errors.Is(err, fault) || frames != 0 || sink.frames != 0 {
		t.Fatal(frames, sink, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if frames, err = submitWeakReference(ctx, &referenceSink{}, samples); !errors.Is(err, context.Canceled) || frames != 0 {
		t.Fatal(frames, err)
	}
}

func TestWeakReferenceRejectsWrongFormatLevelsChannelsAndMissingTail(t *testing.T) {
	for _, mode := range []string{"size", "rate", "right", "level", "silence", "tail"} {
		t.Run(mode, func(t *testing.T) {
			path := weakReferenceFixture(t)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "size":
				b = b[:100]
			case "rate":
				binary.LittleEndian.PutUint32(b[24:], 44100)
			case "right":
				binary.LittleEndian.PutUint16(b[46:], 1)
			case "level":
				binary.LittleEndian.PutUint16(b[44:], 329)
			case "silence":
				clear(b[44:])
			case "tail":
				binary.LittleEndian.PutUint16(b[len(b)-4:], 1)
			}
			if err = os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = loadWeakReference(path); err == nil {
				t.Fatal("unsafe or incompatible reference accepted")
			}
		})
	}
}
