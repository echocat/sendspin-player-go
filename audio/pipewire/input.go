//go:build linux && (amd64 || arm64)

package pipewire

import (
	"context"
	"encoding/binary"
	"math"
	"time"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/internal/capture"
	"github.com/echocat/sendspin-player-go/audio/internal/render"
	log "github.com/echocat/slf4g"
)

// Input selects an explicit capture stream. Loopback opts into a render-sink
// monitor; normal source capture never enables monitoring or local playback.
type Input struct {
	Device, SharedLibPath                           string
	Loopback                                        bool
	BufferDuration, StartupTimeout, MaxTimingWindow time.Duration
	Logger                                          log.Logger
}

func (o Input) Open(ctx context.Context, f audio.Format) (audio.InputDevice, error) {
	return capture.Open(ctx, capture.Config{Backend: "pipewire", Device: o.Device, BufferDuration: o.BufferDuration, StartupTimeout: o.StartupTimeout, MaxTimingWindow: o.MaxTimingWindow, Logger: o.Logger}, f, func(d *capture.Device) (capture.Driver, error) {
		return openStream(o.Device, o.SharedLibPath, nil, d, o.Loopback)
	})
}
func headerFormat() []byte {
	b := make([]byte, 64)
	put := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	put(0, 56)
	put(4, 15)
	put(8, 0x40005)
	put(12, 6)
	put(16, 1)
	put(24, 4)
	put(28, 3)
	put(32, 1)
	put(40, 2)
	put(48, 4)
	put(52, 4)
	put(56, 32)
	return b
}
func captureTime(t pwTime, frames, rate uint32) int64 {
	if t.Now <= 0 || t.Rate.Num == 0 || t.Rate.Denom == 0 || rate == 0 || t.Queued > uint64(rate)*2 || t.Buffered > uint64(rate)*2 {
		return 0
	}
	delay := float64(max(t.Delay, 0))*1e9*float64(t.Rate.Num)/float64(t.Rate.Denom) + (float64(t.Queued+t.Buffered)+float64(frames))*1e9/float64(rate)
	first := float64(t.Now) - delay
	if math.IsNaN(first) || math.IsInf(first, 0) || first <= 0 || first >= float64(math.MaxInt64) {
		return 0
	}
	return int64(first)
}
func (p *driver) processInput() {
	b := p.api.dequeue(p.stream)
	if b == nil {
		return
	}
	queue := func() {
		b.Size = 0
		if p.api.queue(p.stream, b) < 0 {
			p.fail(render.ErrDriver)
		}
	}
	if b.Buffer == nil || b.Buffer.NDatas != 1 || b.Buffer.Datas == nil {
		p.fail(render.ErrDriver)
		queue()
		return
	}
	data := b.Buffer.Datas
	format := p.input.Format()
	stride := uint32(format.Channels * 4)
	if data.Data == nil || data.Chunk == nil || data.MaxSize == 0 {
		p.fail(render.ErrDriver)
		queue()
		return
	}
	n := data.Chunk.Size
	if n == 0 {
		queue()
		return
	}
	if n%stride != 0 || n > data.MaxSize || n/4 > uint32(p.input.MaxSamples()) || (data.Chunk.Stride != 0 && data.Chunk.Stride != int32(stride)) || data.MaxSize > uint32(format.SampleRate*format.Channels*8) {
		p.fail(render.ErrDriver)
		queue()
		return
	}
	var header *metaHeader
	if b.Buffer.NMetas > 0 && b.Buffer.NMetas <= 64 && b.Buffer.Metas != nil {
		for _, m := range unsafe.Slice(b.Buffer.Metas, int(b.Buffer.NMetas)) {
			if m.Type == 1 && m.Size >= 32 && m.Data != nil {
				header = (*metaHeader)(m.Data)
				break
			}
		}
	}
	if header != nil && header.Flags&2 != 0 {
		p.fail(render.ErrDriver)
		queue()
		return
	}
	if header != nil && header.Flags&1 != 0 && p.seen {
		p.fail(capture.ErrOverrun)
		queue()
		return
	}
	var first int64
	if header != nil && header.PTS > 0 {
		first = header.PTS
	} else {
		var t pwTime
		if p.api.time(p.stream, &t, uint64(unsafe.Sizeof(t))) == 0 {
			first = captureTime(t, n/stride, uint32(format.SampleRate))
		}
	}
	if first == 0 {
		queue()
		return
	}
	count := int(n / 4)
	if header != nil && header.Flags&16 != 0 {
		p.input.Process(p.silence[:count], first)
	} else {
		offset := data.Chunk.Offset % data.MaxSize
		raw := unsafe.Slice((*byte)(data.Data), int(data.MaxSize))
		dst := unsafe.Slice((*byte)(unsafe.Pointer(&p.scratch[0])), count*4)
		part := min(int(n), len(raw)-int(offset))
		copy(dst, raw[int(offset):int(offset)+part])
		copy(dst[part:], raw[:int(n)-part])
		p.input.Process(p.scratch[:count], first)
	}
	p.seen = true
	queue()
}
