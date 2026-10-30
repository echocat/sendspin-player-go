package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
)

const maxWaveBytes = 64 << 20
const maxFrames = 16 << 20

type wave struct {
	Rate      int       `json:"sample_rate"`
	Channels  int       `json:"channels"`
	Channel   int       `json:"selected_channel"`
	Bits      int       `json:"container_bits"`
	ValidBits int       `json:"valid_bits"`
	Encoding  string    `json:"encoding"`
	SHA256    string    `json:"sha256"`
	Frames    int       `json:"frames"`
	FullScale int       `json:"full_scale_samples"`
	Samples   []float32 `json:"-"`
}

func readWave(ctx context.Context, path string, channel int) (wave, error) {
	f, err := os.Open(path)
	if err != nil {
		return wave{}, err
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil {
		return wave{}, err
	}
	if !stat.Mode().IsRegular() || stat.Size() < 44 || stat.Size() > maxWaveBytes {
		return wave{}, errors.New("WAV must be a regular RIFF file of 44 bytes..64 MiB")
	}
	return decodeWave(ctx, f, stat.Size(), channel)
}

func decodeWave(ctx context.Context, input io.Reader, size int64, channel int) (wave, error) {
	var result wave
	if size < 12 || size > maxWaveBytes || channel < 0 {
		return result, errors.New("invalid WAV bounds/channel")
	}
	digest := sha256.New()
	r := io.TeeReader(io.LimitReader(input, size), digest)
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return result, err
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" || int64(binary.LittleEndian.Uint32(header[4:8]))+8 != size {
		return result, errors.New("unsupported or inconsistent RIFF/WAVE container (RF64/RIFX unsupported)")
	}
	remaining := size - 12
	haveFormat, haveData := false, false
	var tag, align int
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if remaining < 8 {
			return result, errors.New("truncated RIFF chunk header")
		}
		var chunk [8]byte
		if _, err := io.ReadFull(r, chunk[:]); err != nil {
			return result, err
		}
		remaining -= 8
		n := int64(binary.LittleEndian.Uint32(chunk[4:]))
		padded := n + n%2
		if padded > remaining {
			return result, errors.New("RIFF chunk exceeds container")
		}
		switch string(chunk[:4]) {
		case "fmt ":
			if haveFormat || haveData || n < 16 || n == 17 || n > 4096 {
				return result, errors.New("invalid/duplicate/out-of-order WAV format")
			}
			data := make([]byte, int(n))
			if _, err := io.ReadFull(r, data); err != nil {
				return result, err
			}
			tag = int(binary.LittleEndian.Uint16(data))
			result.Channels = int(binary.LittleEndian.Uint16(data[2:]))
			result.Rate = int(binary.LittleEndian.Uint32(data[4:]))
			align = int(binary.LittleEndian.Uint16(data[12:]))
			result.Bits = int(binary.LittleEndian.Uint16(data[14:]))
			result.ValidBits = result.Bits
			result.Channel = channel
			if tag == 0xfffe {
				if n < 40 || binary.LittleEndian.Uint16(data[16:]) < 22 || int64(binary.LittleEndian.Uint16(data[16:]))+18 > n {
					return result, errors.New("invalid extensible WAV format")
				}
				result.ValidBits = int(binary.LittleEndian.Uint16(data[18:]))
				guid := data[24:40]
				want := []byte{0, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71}
				want[0] = guid[0]
				if string(want) != string(guid) {
					return result, errors.New("unsupported extensible WAV subformat")
				}
				tag = int(guid[0])
			}
			if result.Rate < 8000 || result.Rate > 192000 || result.Channels < 1 || result.Channels > 8 || channel >= result.Channels || align != result.Channels*(result.Bits/8) || int64(binary.LittleEndian.Uint32(data[8:])) != int64(result.Rate)*int64(align) {
				return result, errors.New("inconsistent WAV format or selected channel")
			}
			if tag == 1 && (result.Bits == 16 || result.Bits == 24 || result.Bits == 32) && result.ValidBits > 0 && result.ValidBits <= result.Bits {
				result.Encoding = "integer PCM"
			} else if tag == 3 && result.Bits == 32 && result.ValidBits == 32 {
				result.Encoding = "IEEE float32"
			} else {
				return result, errors.New("supported WAV formats: PCM16/24/32 and float32")
			}
			haveFormat = true
		case "data":
			if !haveFormat || haveData || n == 0 || n%int64(align) != 0 || n/int64(align) > maxFrames {
				return result, errors.New("invalid/duplicate/oversized WAV sample data")
			}
			count := int(n / int64(align))
			result.Samples = make([]float32, count)
			buffer := make([]byte, 4096*align)
			for frame := 0; frame < count; {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				frames := min(4096, count-frame)
				data := buffer[:frames*align]
				if _, err := io.ReadFull(r, data); err != nil {
					return result, err
				}
				for j := 0; j < frames; j++ {
					at := j*align + channel*(result.Bits/8)
					var v float64
					full := false
					if tag == 3 {
						v = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[at:])))
						full = math.Abs(v) >= 1
					} else {
						var value int32
						switch result.Bits {
						case 16:
							value = int32(int16(binary.LittleEndian.Uint16(data[at:])))
						case 24:
							value = int32(uint32(data[at]) | uint32(data[at+1])<<8 | uint32(data[at+2])<<16)
							value = value << 8 >> 8
						case 32:
							value = int32(binary.LittleEndian.Uint32(data[at:]))
						}
						if result.ValidBits < result.Bits {
							shift := result.Bits - result.ValidBits
							if uint32(value)&((uint32(1)<<uint(shift))-1) != 0 {
								return result, errors.New("nonzero padding bits in extensible PCM sample")
							}
							value >>= result.Bits - result.ValidBits
						}
						scale := math.Ldexp(1, result.ValidBits-1)
						v = float64(value) / scale
						full = float64(value) == -scale || float64(value) == scale-1
					}
					if math.IsNaN(v) || math.IsInf(v, 0) || v < -1 || v > 1 {
						return result, errors.New("nonfinite or out-of-range WAV sample")
					}
					if full {
						result.FullScale++
					}
					result.Samples[frame+j] = float32(v)
				}
				frame += frames
			}
			haveData = true
		default:
			if _, err := io.CopyN(io.Discard, r, n); err != nil {
				return result, err
			}
		}
		if n%2 != 0 {
			var pad [1]byte
			if _, err := io.ReadFull(r, pad[:]); err != nil {
				return result, err
			}
		}
		remaining -= padded
	}
	if !haveData {
		return result, errors.New("WAV has no sample data")
	}
	result.Frames = len(result.Samples)
	result.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}

func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func writeWave(ctx context.Context, path string, rate, channels, selected int, samples []float32) (sum string, err error) {
	if rate < 8000 || rate > 192000 || channels < 1 || channels > 8 || selected < 0 || selected >= channels || len(samples) == 0 || len(samples) > maxFrames || int64(len(samples))*int64(channels)*2+44 > maxWaveBytes {
		return "", errors.New("invalid generated WAV bounds")
	}
	f, err := createPrivate(path)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	digest := sha256.New()
	w := bufio.NewWriter(io.MultiWriter(f, digest))
	if err = encodeWave(ctx, w, rate, channels, selected, samples); err != nil {
		return "", err
	}
	if err = w.Flush(); err != nil {
		return "", err
	}
	return finishHash(digest), nil
}

func finishHash(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
func encodeWave(ctx context.Context, w io.Writer, rate, channels, selected int, samples []float32) error {
	var h [44]byte
	copy(h[:], "RIFF")
	n := len(samples) * channels * 2
	binary.LittleEndian.PutUint32(h[4:], uint32(n+36))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(n))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	buffer := make([]byte, 4096*channels*2)
	for first := 0; first < len(samples); {
		if err := ctx.Err(); err != nil {
			return err
		}
		frames := min(4096, len(samples)-first)
		data := buffer[:frames*channels*2]
		clear(data)
		for i, sample := range samples[first : first+frames] {
			v := float64(sample)
			if math.IsNaN(v) || math.IsInf(v, 0) || v < -1 || v > 1 {
				return fmt.Errorf("invalid generated sample at frame %d", first+i)
			}
			q := int16(max(-32768, min(32767, math.Round(v*32768))))
			binary.LittleEndian.PutUint16(data[(i*channels+selected)*2:], uint16(q))
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		first += frames
	}
	return nil
}
