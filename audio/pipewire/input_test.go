//go:build linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
)

func TestInputMetaPODAndCaptureClockDomains(t *testing.T) {
	const expected = "380000000f0000000500040006000000010000000000000004000000030000000100000000000000020000000000000004000000040000002000000000000000"
	if hex.EncodeToString(headerFormat()) != expected || unsafe.Sizeof(spaMeta{}) != 16 || unsafe.Sizeof(metaHeader{}) != 32 {
		t.Fatal("input metadata differs from C ABI vector")
	}
	if at := captureTime(pwTime{Now: 1e9, Rate: fraction{1, 48000}, Delay: 480, Buffered: 480}, 480, 48000); at != 970000000 {
		t.Fatal("capture clock did not subtract native delays", at)
	}
	if captureTime(pwTime{}, 480, 48000) != 0 {
		t.Fatal("capture invented a timestamp")
	}
}

func TestPipeWireCaptureBufferPreservationWrapAndDiscontinuity(t *testing.T) {
	for _, offset := range []uint32{0, 8} {
		t.Run("offset"+string(rune('0'+offset)), func(t *testing.T) {
			original := []float32{.5, 0, -.25, 0}
			chunk := spaChunk{Offset: offset, Size: 16, Stride: 8}
			data := spaData{MaxSize: 16, Data: unsafe.Pointer(&original[0]), Chunk: &chunk}
			header := metaHeader{}
			meta := spaMeta{Type: 1, Size: 32, Data: unsafe.Pointer(&header)}
			buffer := spaBuffer{NDatas: 1, Datas: &data, NMetas: 1, Metas: &meta}
			native := pwBuffer{Buffer: &buffer}
			queued := 0
			pending := true
			p := &driver{}
			p.state.Store(2)
			p.api = &api{loopLock: func(uintptr) {}, loopUnlock: func(uintptr) {}, streamFlush: func(uintptr, bool) int32 { return 0 }}
			p.api.dequeue = func(uintptr) *pwBuffer {
				if !pending {
					return nil
				}
				pending = false
				return &native
			}
			p.api.queue = func(uintptr, *pwBuffer) int32 { queued++; return 0 }
			p.api.streamActive = func(_ uintptr, active bool) int32 {
				if active {
					header.PTS = bridge.Now() - int64(time.Millisecond)
					p.process()
				}
				return 0
			}
			d, err := capture.Open(context.Background(), capture.Config{Backend: "fake PW input", MaxTimingWindow: 50 * time.Millisecond}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *capture.Device) (capture.Driver, error) {
				p.input = d
				p.scratch = make([]float32, d.MaxSamples())
				p.silence = make([]float32, d.MaxSamples())
				return p, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = d.Close() }()
			got := make([]float32, 4)
			n, at, err := d.Read(context.Background(), got)
			want := []float32{.5, 0, -.25, 0}
			if offset != 0 {
				want = []float32{-.25, 0, .5, 0}
			}
			if err != nil || n != 4 || at.IsZero() || !slices.Equal(got, want) || !slices.Equal(original, []float32{.5, 0, -.25, 0}) || queued != 1 || native.Size != 0 {
				t.Fatal("capture changed samples or did not return its native buffer", n, err, got, queued)
			}
			pending = true
			header.Flags = 1
			p.process()
			if _, _, err = d.Read(context.Background(), got); !errors.Is(err, capture.ErrOverrun) {
				t.Fatal("capture hid a discontinuity", err)
			}
		})
	}
}
func TestPipeWireInputExplicitLibraryAndCancellation(t *testing.T) {
	o := Input{SharedLibPath: filepath.Join(t.TempDir(), "missing-input-pipewire.so")}
	if _, err := o.Open(context.Background(), audio.Format{SampleRate: 48000, Channels: 2}); err == nil {
		t.Fatal("explicit capture library failure ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Open(ctx, audio.Format{SampleRate: 48000, Channels: 2}); err != context.Canceled {
		t.Fatal(err)
	}
}
