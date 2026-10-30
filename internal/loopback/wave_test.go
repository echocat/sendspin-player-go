package loopback

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
)

func rawWave(format, data []byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WAVEfmt ")
	b = binary.LittleEndian.AppendUint32(b, uint32(len(format)))
	b = append(b, format...)
	if len(format)%2 != 0 {
		b = append(b, 0)
	}
	b = append(b, []byte("data")...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 != 0 {
		b = append(b, 0)
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}
func rawFormat(tag, bits, channels int) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b, uint16(tag))
	binary.LittleEndian.PutUint16(b[2:], uint16(channels))
	binary.LittleEndian.PutUint32(b[4:], 48000)
	binary.LittleEndian.PutUint32(b[8:], uint32(48000*channels*bits/8))
	binary.LittleEndian.PutUint16(b[12:], uint16(channels*bits/8))
	binary.LittleEndian.PutUint16(b[14:], uint16(bits))
	return b
}

func TestWAVIntegerFloatAndExtensibleContainers(t *testing.T) {
	for _, test := range []struct {
		name         string
		format, data []byte
		want         []float32
		full         int
	}{
		{"pcm16", rawFormat(1, 16, 1), []byte{0, 128, 0, 0, 255, 127}, []float32{-1, 0, 32767.0 / 32768}, 2},
		{"pcm24", rawFormat(1, 24, 1), []byte{0, 0, 128, 0, 0, 0, 255, 255, 127}, []float32{-1, 0, 8388607.0 / 8388608}, 2},
		{"pcm32", rawFormat(1, 32, 1), []byte{0, 0, 0, 128, 0, 0, 0, 0, 0, 0, 0, 64}, []float32{-1, 0, .5}, 1},
		{"float32", rawFormat(3, 32, 1), []byte{0, 0, 0, 191, 0, 0, 0, 0, 0, 0, 0, 63}, []float32{-.5, 0, .5}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := rawWave(test.format, test.data)
			w, err := decodeWave(context.Background(), bytes.NewReader(b), int64(len(b)), 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(w.Samples) != len(test.want) || w.FullScale != test.full {
				t.Fatal(w)
			}
			for i, want := range test.want {
				if w.Samples[i] != want {
					t.Fatal(i, w.Samples[i], want)
				}
			}
		})
	}
	format := append(rawFormat(0xfffe, 32, 1), make([]byte, 24)...)
	binary.LittleEndian.PutUint16(format[16:], 22)
	binary.LittleEndian.PutUint16(format[18:], 24)
	copy(format[24:], []byte{1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71})
	b := rawWave(format, []byte{0, 0, 0, 64, 0, 0, 0, 128})
	w, err := decodeWave(context.Background(), bytes.NewReader(b), int64(len(b)), 0)
	if err != nil || w.ValidBits != 24 || len(w.Samples) != 2 || w.Samples[0] != .5 || w.Samples[1] != -1 {
		t.Fatal("24 valid bits in 32-bit container", w, err)
	}
}

func TestWAVSelectionDoesNotDownmixAndReaderRejectsMalformedInput(t *testing.T) {
	b := rawWave(rawFormat(1, 16, 2), []byte{0, 64, 0, 192})
	left, err := decodeWave(context.Background(), bytes.NewReader(b), int64(len(b)), 0)
	if err != nil {
		t.Fatal(err)
	}
	right, err := decodeWave(context.Background(), bytes.NewReader(b), int64(len(b)), 1)
	if err != nil || left.Samples[0] != .5 || right.Samples[0] != -.5 {
		t.Fatal("channels mixed", left, right, err)
	}
	for _, name := range []string{"truncated", "riff-length", "data-length", "rate", "align", "channel", "nan", "infinity", "outside", "guid"} {
		t.Run(name, func(t *testing.T) {
			bad := append([]byte(nil), b...)
			channel := 0
			switch name {
			case "truncated":
				bad = bad[:len(bad)-1]
			case "riff-length":
				binary.LittleEndian.PutUint32(bad[4:], 1)
			case "data-length":
				binary.LittleEndian.PutUint32(bad[40:], 99)
			case "rate":
				binary.LittleEndian.PutUint32(bad[24:], 0)
			case "align":
				binary.LittleEndian.PutUint16(bad[32:], 0)
			case "channel":
				channel = 2
			case "nan", "infinity", "outside":
				var bits uint32
				switch name {
				case "nan":
					bits = math.Float32bits(float32(math.NaN()))
				case "infinity":
					bits = math.Float32bits(float32(math.Inf(1)))
				case "outside":
					bits = math.Float32bits(1.2)
				}
				data := binary.LittleEndian.AppendUint32(nil, bits)
				bad = rawWave(rawFormat(3, 32, 1), data)
			case "guid":
				format := append(rawFormat(0xfffe, 32, 1), make([]byte, 24)...)
				binary.LittleEndian.PutUint16(format[16:], 22)
				bad = rawWave(format, []byte{0, 0, 0, 0})
			}
			if _, err := decodeWave(context.Background(), bytes.NewReader(bad), int64(len(bad)), channel); err == nil {
				t.Fatal("malformed WAV accepted", name)
			}
		})
	}
}

func TestWAVRoundtripHashPaddingAndCancellation(t *testing.T) {
	var encoded bytes.Buffer
	samples := []float32{-.5, 0, .5}
	if err := encodeWave(context.Background(), &encoded, 48000, 2, 0, samples); err != nil {
		t.Fatal(err)
	}
	w, err := decodeWave(context.Background(), bytes.NewReader(encoded.Bytes()), int64(encoded.Len()), 0)
	if err != nil || w.SHA256 == "" {
		t.Fatal(err)
	}
	// Independent odd-length ancillary chunk before the normal format chunk.
	b := append([]byte(nil), encoded.Bytes()[:12]...)
	b = append(b, []byte("JUNK\x01\x00\x00\x00x\x00")...)
	b = append(b, encoded.Bytes()[12:]...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	padded, err := decodeWave(context.Background(), bytes.NewReader(b), int64(len(b)), 0)
	if err != nil || padded.SHA256 == w.SHA256 || padded.Samples[2] != .5 {
		t.Fatal("chunk padding/hash", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := decodeWave(ctx, bytes.NewReader(b), int64(len(b)), 0); err == nil {
		t.Fatal("canceled read succeeded")
	}
	// Every truncation is bounded and must return an error, not panic or succeed.
	for n := 0; n < encoded.Len(); n++ {
		data := encoded.Bytes()[:n]
		if _, err := decodeWave(context.Background(), bytes.NewReader(data), int64(len(data)), 0); err == nil {
			t.Fatal("truncation accepted", n)
		}
	}
}

func TestWAVHeaderMutationsStayBounded(t *testing.T) {
	valid := rawWave(rawFormat(1, 24, 2), []byte{0, 0, 0, 0, 0, 0})
	for at := 12; at < len(valid); at++ {
		for _, value := range []byte{0, 1, 0x7f, 0x80, 0xff} {
			mutated := append([]byte(nil), valid...)
			mutated[at] = value
			w, err := decodeWave(context.Background(), bytes.NewReader(mutated), int64(len(mutated)), 0)
			if err == nil && (len(w.Samples) > maxFrames || w.Rate < 8000 || w.Rate > 192000 || w.Channels < 1 || w.Channels > 8) {
				t.Fatal("mutation bypassed bounds", at, value)
			}
		}
	}
	// A plausible large data declaration must fail before allocating its decoded
	// sample slice or reading absent bytes. The whole RIFF stays below 64 MiB.
	large := rawWave(rawFormat(1, 16, 1), []byte{0, 0})
	n := uint32((maxFrames + 1) * 2)
	binary.LittleEndian.PutUint32(large[40:], n)
	binary.LittleEndian.PutUint32(large[4:], n+36)
	if _, err := decodeWave(context.Background(), bytes.NewReader(large), int64(n)+44, 0); err == nil {
		t.Fatal("sample allocation bound bypassed")
	}
}
