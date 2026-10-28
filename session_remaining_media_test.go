package sendspin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/playback"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

func proofSession(t *testing.T) *session {
	t.Helper()
	cfg := schemaConfig()
	cfg.Store = &MemoryStore{}
	c, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(c, context.Background(), func() {})
	s.channel = &wire.Channel{ServerID: c.ClientID(), Category: "lt"}
	s.hello, s.activated, s.admitted = true, true, true
	s.state.ActiveRoles, s.state.Available, s.state.Volume = slices.Clone(cfg.Roles), true, 100
	for _, r := range cfg.Roles {
		family, _, _ := strings.Cut(string(r), "@")
		s.active[family] = r
	}
	for _, at := range []int64{1, 2, 3} {
		s.filter.Update(0, 1, at)
	}
	c.active = s
	return s
}

type proofWrite struct {
	format  audio.Format
	samples []float32
}
type proofOutput struct {
	mu     sync.Mutex
	next   time.Time
	writes chan proofWrite
}
type proofDevice struct {
	output *proofOutput
	format audio.Format
}

func (p *proofOutput) Open(_ context.Context, f audio.Format) (audio.Device, error) {
	return &proofDevice{p, f}, nil
}
func (d *proofDevice) Next(ctx context.Context) (time.Time, error) {
	d.output.mu.Lock()
	defer d.output.mu.Unlock()
	return d.output.next, ctx.Err()
}
func (d *proofDevice) Write(ctx context.Context, x []float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.output.mu.Lock()
	d.output.next = d.output.next.Add(time.Duration(len(x)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
	d.output.mu.Unlock()
	select {
	case d.output.writes <- proofWrite{d.format, slices.Clone(x)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*proofDevice) Flush(context.Context) error { return nil }
func (*proofDevice) Close() error                { return nil }

func proofAudio(stamp int64, ahead uint32, data []byte) []byte {
	b := binary.BigEndian.AppendUint64([]byte{4}, uint64(stamp))
	b = binary.BigEndian.AppendUint32(b, ahead)
	return append(b, data...)
}
func proofFLAC(t *testing.T) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	e, err := flac.NewEncoder(&b, &meta.StreamInfo{SampleRate: 8000, NChannels: 1, BitsPerSample: 16, BlockSizeMin: 160, BlockSizeMax: 160})
	if err != nil {
		t.Fatal(err)
	}
	header := bytes.Clone(b.Bytes())
	samples := make([]int32, 160)
	for i := range samples {
		samples[i] = 16384
	}
	err = e.WriteFrame(&frame.Frame{Header: frame.Header{BlockSize: 160, SampleRate: 8000, Channels: frame.ChannelsMono, BitsPerSample: 16}, Subframes: []*frame.Subframe{{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: samples, NSamples: 160}}})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(header), bytes.Clone(b.Bytes()[len(header):])
}
func runProofEngine(t *testing.T, s *session, out *proofOutput, count int) []proofWrite {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.engine.Run(ctx) }()
	var writes []proofWrite
	for range count {
		select {
		case w := <-out.writes:
			writes = append(writes, w)
		case <-ctx.Done():
			t.Fatal("render worker did not produce expected chunks")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	return writes
}

func TestBufferedCodecSwitchUsesEachChunksReceiveFormat(t *testing.T) {
	s := proofSession(t)
	s.client.cfg.Formats = []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}, {Codec: "flac", SampleRate: 8000, Channels: 1, BitDepth: 16}, {Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 24}}
	anchor := time.Now()
	out := &proofOutput{next: anchor, writes: make(chan proofWrite, 8)}
	s.engine = playback.New(out, 1<<20, func(stamp int64) (time.Time, error) {
		return anchor.Add(time.Duration(stamp-1_000_000) * time.Microsecond), nil
	})
	start := func(f codec.Format, h string) {
		p := map[string]any{"codec": f.Codec, "sample_rate": f.SampleRate, "channels": f.Channels, "bit_depth": f.BitDepth}
		if h != "" {
			p["codec_header"] = h
		}
		raw, _ := json.Marshal(map[string]any{"server_transmitted": 0, "player": p})
		if err := s.startStream(raw); err != nil {
			t.Fatal(err)
		}
	}
	start(s.client.cfg.Formats[0], "")
	pcm := bytes.Repeat([]byte{0, 32}, 160)
	if err := s.binary(proofAudio(1_000_000, 0, pcm)); err != nil {
		t.Fatal(err)
	}
	header, payload := proofFLAC(t)
	start(s.client.cfg.Formats[1], header)
	if err := s.binary(proofAudio(1_020_000, 0, payload)); err != nil {
		t.Fatal(err)
	}
	start(s.client.cfg.Formats[2], "")
	if err := s.binary(proofAudio(1_040_000, 0, bytes.Repeat([]byte{0, 0, 224}, 160))); err != nil {
		t.Fatal(err)
	}
	// Change the active decoder again before rendering: queued chunks must not
	// be decoded a second time using this final configuration.
	start(s.client.cfg.Formats[0], "")
	writes := runProofEngine(t, s, out, 3)
	for i, w := range writes {
		want := []float32{.25, .5, -.25}[i]
		if len(w.samples) != 160 {
			t.Fatal("buffered chunk length changed", i, len(w.samples))
		}
		for _, v := range w.samples {
			if v != want {
				t.Fatal("buffered chunk used another codec/depth", i, v, want)
			}
		}
	}
}

func TestSendAheadCannotChangeSchedulingOrClockSamples(t *testing.T) {
	for _, ahead := range []uint32{0, 1, 123456, math.MaxUint32 - 1, math.MaxUint32} {
		s := proofSession(t)
		f := codec.Format{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}
		s.streamFormat = f
		s.streams["player"] = true
		var err error
		s.decoder, err = codec.NewBuiltIn(f, "")
		if err != nil {
			t.Fatal(err)
		}
		mapping, uncertainty := s.filter.ServerTime(123456), s.filter.Error()
		anchor := time.Now()
		out := &proofOutput{next: anchor, writes: make(chan proofWrite, 4)}
		targets := make(chan int64, 4)
		s.engine = playback.New(out, 1<<20, func(stamp int64) (time.Time, error) { targets <- stamp; return anchor, nil })
		if err = s.binary(proofAudio(777_000, ahead, bytes.Repeat([]byte{0, 32}, 160))); err != nil {
			t.Fatal(err)
		}
		writes := runProofEngine(t, s, out, 1)
		if len(targets) != 1 || <-targets != 777_000 || len(writes[0].samples) != 160 || s.filter.ServerTime(123456) != mapping || s.filter.Error() != uncertainty {
			t.Fatal("send_ahead affected scheduling/filter", ahead)
		}
		for _, v := range writes[0].samples {
			if v != .25 {
				t.Fatal("send_ahead edited output", ahead, v)
			}
		}
	}
}

func TestStreamStartWrapperRequiresTypedTimestampAndActiveObjects(t *testing.T) {
	objects := map[string]any{"player": map[string]any{"codec": "pcm", "sample_rate": 8000, "channels": 1, "bit_depth": 24}, "artwork": map[string]any{"channels": schemaConfig().Artwork}, "visualizer": map[string]any{"types": []string{"loudness"}, "rate_max": 9}}
	for family, obj := range objects {
		for _, stamp := range []any{nil, false, "0", 1.5, []any{}, map[string]any{}, json.Number("9223372036854775808")} {
			s := proofSession(t)
			p := map[string]any{"server_transmitted": stamp, family: obj}
			b, _ := json.Marshal(p)
			if err := s.startStream(b); !errors.Is(err, protocol.ErrProtocol) || len(s.streams) != 0 {
				t.Fatal("bad start timestamp opened stream", family, stamp, err)
			}
		}
		for _, stamp := range []int64{math.MinInt64, -1, 0, math.MaxInt64} {
			s := proofSession(t)
			s.state.Available = false
			b, _ := json.Marshal(map[string]any{"server_transmitted": stamp, family: obj})
			if err := s.startStream(b); err != nil || !s.streams[family] {
				t.Fatal("valid unavailable start failed", family, err)
			}
		}
		for _, obj := range []any{obj, nil, false, []any{}, map[string]any{}} {
			s := proofSession(t)
			s.state.ActiveRoles = nil
			b, _ := json.Marshal(map[string]any{"server_transmitted": 0, family: obj})
			if err := s.startStream(b); !errors.Is(err, protocol.ErrProtocol) || len(s.streams) != 0 {
				t.Fatal("inactive known stream object accepted", family, err)
			}
		}
		s := proofSession(t)
		b, _ := json.Marshal(map[string]any{family: obj, "SERVER_TRANSMITTED": 0})
		if err := s.startStream(b); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("missing timestamp accepted", family, err)
		}
	}
}

func TestSelectedPlayerFormatRejectsEveryNonAdvertisedDimension(t *testing.T) {
	for _, f := range []codec.Format{{Codec: "flac", SampleRate: 8000, Channels: 1, BitDepth: 24}, {Codec: "pcm", SampleRate: 16000, Channels: 1, BitDepth: 24}, {Codec: "pcm", SampleRate: 8000, Channels: 2, BitDepth: 24}, {Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}} {
		s := proofSession(t)
		s.client.cfg.Formats = []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 24}}
		called := false
		s.client.cfg.Decoders[f.Codec] = func(codec.Format, string) (codec.AudioDecoder, error) { called = true; return unusedDecoder{}, nil }
		player := map[string]any{"codec": f.Codec, "sample_rate": f.SampleRate, "channels": f.Channels, "bit_depth": f.BitDepth}
		if f.Codec == "flac" {
			player["codec_header"] = "ZkxhQw=="
		}
		b, _ := json.Marshal(map[string]any{"server_transmitted": 0, "player": player})
		if err := s.startStream(b); !errors.Is(err, protocol.ErrProtocol) || called || s.streams["player"] {
			t.Fatal("unadvertised format reached factory", f, err)
		}
	}
	for _, depth := range []int{math.MinInt, -1, 0, 16, 24, math.MaxInt} {
		s := proofSession(t)
		s.client.cfg.Formats = append(s.client.cfg.Formats, codec.Format{Codec: "opus", SampleRate: 48000, Channels: 2, BitDepth: 16})
		called := false
		s.client.cfg.Decoders["opus"] = func(f codec.Format, _ string) (codec.AudioDecoder, error) {
			called = true
			if f.BitDepth != depth {
				t.Fatal("wire depth changed")
			}
			return unusedDecoder{}, nil
		}
		b, _ := json.Marshal(map[string]any{"server_transmitted": 0, "player": codec.Format{Codec: "opus", SampleRate: 48000, Channels: 2, BitDepth: depth}})
		if err := s.startStream(b); err != nil || !called {
			t.Fatal("Opus matching added bit-depth restriction", depth, err)
		}
	}
}

func TestAllFourArtworkDiscriminatorsStayOnTheirChannels(t *testing.T) {
	cfg := schemaConfig()
	cfg.Roles = []Role{ArtworkRole}
	cfg.Artwork[1] = ArtworkChannel{Source: "artist", Format: "png", Width: 32, Height: 32}
	s, p := newSchemaSession(t, cfg)
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, false))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": cfg.Roles})
	if kind, _, err := p.readSecure(); err != nil || kind != "client/state" {
		t.Fatal(err)
	}
	schemaReceive(t, s, p, "stream/start", map[string]any{"server_transmitted": 0, "artwork": map[string]any{"channels": cfg.Artwork}})
	for channel := range byte(4) {
		schemaBinary(t, s, p, artworkAnnounce(channel, 0, 2))
		schemaBinary(t, s, p, []byte{8 + channel, 0, channel, 100 + channel})
		for seen := byte(0); seen <= channel; seen++ {
			image, ok := s.client.CurrentArtwork(int(seen))
			if !ok || image.Channel != int(seen) || !bytes.Equal(image.Data, []byte{seen, 100 + seen}) {
				t.Fatal("crossed artwork channels", seen, image)
			}
		}
	}
}

func TestFundamentalFrequencyIsNotInferredFromDominantBin(t *testing.T) {
	s := visualizerSession(t)
	startVisualizerTestStream(t, s)
	stamp := s.now() + 1_000_000
	if err := s.visualization(visualizerFrame(18, stamp, 3, 112, 0x80, 1)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.media.visualQueue[0].event.Values, []uint16{880, 32769}) {
		t.Fatal("dominant harmonic was converted to an inferred note/fundamental")
	}
}
