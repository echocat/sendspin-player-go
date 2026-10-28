package codec

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

func TestPCMRetainsDistinctInterleavedChannels(t *testing.T) {
	for _, depth := range []int{16, 24} {
		for _, channels := range []int{2, 3, 8} {
			var data []byte
			var want []float32
			for frame := range 5 {
				for channel := range channels {
					v := int32((frame+1)*1000 + channel*37)
					if channel%2 != 0 {
						v = -v
					}
					if depth == 16 {
						data = binary.LittleEndian.AppendUint16(data, uint16(v))
						want = append(want, float32(v)/32768)
					} else {
						data = append(data, byte(v), byte(v>>8), byte(v>>16))
						want = append(want, float32(v)/8388608)
					}
				}
			}
			d, err := New(Format{"pcm", 8000, channels, depth}, "")
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Decode(data)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("channel/frame order changed", depth, channels, got, err)
			}
		}
	}
}

func TestOpusFormatMatchingIgnoresOnlyBitDepth(t *testing.T) {
	for _, depth := range []int{math.MinInt, -1, 0, 16, 24, 32, math.MaxInt} {
		f := Format{"opus", 48000, 2, depth}
		if !f.Matches(Format{"opus", 48000, 2, 16}) || f.Validate() != nil {
			t.Fatal("Opus acquired a bit-depth constraint", depth)
		}
		for _, other := range []Format{{"pcm", 48000, 2, depth}, {"opus", 24000, 2, depth}, {"opus", 48000, 1, depth}} {
			if f.Matches(other) {
				t.Fatal("Opus ignored a codec/rate/channel difference", other)
			}
		}
	}
	if (Format{"pcm", 48000, 2, 16}).Matches(Format{"pcm", 48000, 2, 24}) {
		t.Fatal("Opus exception leaked to PCM")
	}
}

func TestOpusBarePacketFramingAndContainers(t *testing.T) {
	for _, tc := range []struct {
		packet []byte
		frames int
	}{
		{[]byte{0xf8, 0xff, 0xfe}, 960},
		{[]byte{0xf9, 0xff, 0xfe, 0xff, 0xfe}, 1920},
		{[]byte{0xfa, 2, 0xff, 0xfe, 0xff, 0xfe}, 1920},
		{[]byte{0xfb, 3, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe}, 2880},
		{[]byte{0xfb, 6, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe}, 5760},
	} {
		d, err := NewBuiltIn(Format{"opus", 48000, 2, 0}, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.Decode(tc.packet)
		if err != nil || len(got) != 2*tc.frames {
			t.Fatal("valid bare packet/multiframe packet failed", tc.packet, len(got), err)
		}
	}
	for _, packet := range [][]byte{nil, {}, {0xf9, 1}, {0xfa}, {0xfa, 252}, {0xfb}, {0xfb, 0}, {0xfb, 7, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe, 0xff, 0xfe}, []byte("OggS\x00\x02"), []byte("OpusHead\x01\x02"), []byte("OpusTags")} {
		d, err := NewBuiltIn(Format{"opus", 48000, 2, 16}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.Decode(packet); err == nil {
			t.Fatal("invalid/container/over-duration packet accepted", packet)
		}
	}
	// Arbitrary concatenated bytes may themselves form one valid Opus packet;
	// this is packet-framing evidence, not an impossible provenance detector.
}
