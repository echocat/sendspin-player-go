package codec

import (
	"bytes"
	"encoding/base64"
	"math"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

func TestPCM(t *testing.T) {
	for _, tc := range []struct {
		depth int
		data  []byte
	}{{16, []byte{0, 128, 255, 127, 0, 0}}, {24, []byte{0, 0, 128, 255, 255, 127, 0, 0, 0}}} {
		d, err := New(Format{"pcm", 48000, 1, tc.depth}, "")
		if err != nil {
			t.Fatal(err)
		}
		x, err := d.Decode(tc.data)
		if err != nil || len(x) != 3 || x[0] != -1 || x[1] < 0.99 || x[2] != 0 {
			t.Fatal(x, err)
		}
		if _, err = d.Decode(tc.data[:1]); err == nil {
			t.Fatal("accepted partial frame")
		}
	}
}

func flacFixture(t *testing.T) (Format, string, []byte) {
	t.Helper()
	var b bytes.Buffer
	info := &meta.StreamInfo{SampleRate: 48000, NChannels: 1, BitsPerSample: 16, BlockSizeMin: 960, BlockSizeMax: 960}
	enc, err := flac.NewEncoder(&b, info)
	if err != nil {
		t.Fatal(err)
	}
	start := b.Len()
	samples := make([]int32, 960)
	for i := range samples {
		samples[i] = int32(2000 * math.Sin(float64(i)*0.1))
	}
	f := &frame.Frame{Header: frame.Header{BlockSize: 960, SampleRate: 48000, Channels: frame.ChannelsMono, BitsPerSample: 16}, Subframes: []*frame.Subframe{{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: samples, NSamples: 960}}}
	if err = enc.WriteFrame(f); err != nil {
		t.Fatal(err)
	}
	header := append([]byte(nil), b.Bytes()[:start]...)
	payload := append([]byte(nil), b.Bytes()[start:]...)
	return Format{"flac", 48000, 1, 16}, base64.StdEncoding.EncodeToString(header), payload
}
func TestFLAC(t *testing.T) {
	f, h, p := flacFixture(t)
	d, err := New(f, h)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := d.Decode(p)
	if err != nil || len(samples) != 960 {
		t.Fatal(len(samples), err)
	}
	for i, v := range samples {
		want := float32(int32(2000*math.Sin(float64(i)*0.1))) / 32768
		if v != want {
			t.Fatalf("sample %d got %v want %v", i, v, want)
		}
	}
	if _, err = d.Decode(p[:len(p)-1]); err == nil {
		t.Fatal("accepted truncated FLAC")
	}
}

func TestFLACHeaderBase64RejectsIgnoredLineBreaks(t *testing.T) {
	format, header, _ := flacFixture(t)
	for _, separator := range []string{"\r", "\n", "\r\n"} {
		for _, bad := range []string{separator + header, header[:8] + separator + header[8:], header + separator} {
			if _, err := New(format, bad); err == nil {
				t.Fatal("accepted non-alphabet characters in standard Base64 codec_header")
			}
		}
	}
	if _, err := New(format, header); err != nil {
		t.Fatal("rejected canonical standard Base64 header", err)
	}
}
func FuzzPCM(f *testing.F) {
	f.Add([]byte{0, 0}, 16)
	f.Fuzz(func(t *testing.T, b []byte, depth int) {
		if depth != 16 && depth != 24 {
			return
		}
		d, err := New(Format{"pcm", 48000, 2, depth}, "")
		if err != nil {
			t.Fatal(err)
		}
		x, err := d.Decode(b)
		if err == nil {
			for _, v := range x {
				if v < -1 || v >= 1 || math.IsNaN(float64(v)) {
					t.Fatal(v)
				}
			}
		}
	})
}

func TestOpusPacketDecoder(t *testing.T) {
	format := Format{Codec: "opus", SampleRate: 48000, Channels: 2, BitDepth: 16}
	d, err := NewBuiltIn(format, "")
	if err != nil {
		t.Fatal(err)
	}
	// Common RFC 6716 Opus silence packet, no Ogg or OpusHead wrapper.
	first, err := d.Decode([]byte{0xf8, 0xff, 0xfe})
	if err != nil || len(first) != 960*2 {
		t.Fatal(len(first), err)
	}
	before := append([]float32(nil), first...)
	if _, err = d.Decode([]byte{0xf8, 0xff, 0xfe}); err != nil {
		t.Fatal(err)
	}
	for i, v := range first {
		if v != before[i] {
			t.Fatal("decoder overwrote queued output")
		}
	}
	if _, err = d.Decode(nil); err == nil {
		t.Fatal("accepted empty packet as PLC")
	}
	if _, err = NewBuiltIn(format, "T2dnUw=="); err == nil {
		t.Fatal("accepted container header")
	}
}
