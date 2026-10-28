// Package codec provides bounded, pure-Go PCM, FLAC and Opus decoding.
package codec

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/mewkiz/flac"
)

type Format struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	BitDepth   int    `json:"bit_depth"`
}

// Matches ignores bit depth for Opus, as required by the wire specification.
func (f Format) Matches(other Format) bool {
	if f.Codec != other.Codec || f.SampleRate != other.SampleRate || f.Channels != other.Channels {
		return false
	}
	return f.Codec == "opus" || f.BitDepth == other.BitDepth
}

func (f Format) Validate() error {
	if f.Codec == "opus" {
		if (f.Channels == 1 || f.Channels == 2) && (f.SampleRate == 8000 || f.SampleRate == 12000 || f.SampleRate == 16000 || f.SampleRate == 24000 || f.SampleRate == 48000) {
			return nil
		}
		return errors.New("sendspin: unsupported Opus format")
	}
	if (f.Codec != "pcm" && f.Codec != "flac") || f.SampleRate < 8000 || f.SampleRate > 384000 || f.Channels < 1 || f.Channels > 8 || (f.BitDepth != 16 && f.BitDepth != 24) {
		return fmt.Errorf("sendspin: unsupported audio format")
	}
	return nil
}

type Decoder struct {
	format Format
	header []byte
}

// AudioDecoder retains codec history for one stream format. A new stream or
// stream/clear must reset it; decode is called serially with exactly one chunk.
type AudioDecoder interface {
	Decode([]byte) ([]float32, error)
}

// Factory creates a decoder. The SDK checks codec-header presence and canonical
// standard Base64 before dispatch; a custom factory validates codec contents.
type Factory func(Format, string) (AudioDecoder, error)

// NewBuiltIn creates a decoder for PCM, FLAC or Opus.
func NewBuiltIn(f Format, header string) (AudioDecoder, error) {
	if f.Codec == "opus" {
		return newOpus(f, header)
	}
	return New(f, header)
}

func New(format Format, header string) (*Decoder, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	if format.Codec == "opus" {
		return nil, errors.New("sendspin: use NewBuiltIn for the stateful Opus decoder")
	}
	d := &Decoder{format: format}
	if format.Codec == "pcm" {
		if header != "" {
			return nil, errors.New("sendspin: PCM must not carry a codec header")
		}
		return d, nil
	}
	b, err := base64.StdEncoding.Strict().DecodeString(header)
	// RC1 permits the stream marker and STREAMINFO only, not arbitrary metadata.
	if err != nil || base64.StdEncoding.EncodeToString(b) != header || len(b) != 42 || string(b[:4]) != "fLaC" || b[4]&0x7f != 0 || b[5] != 0 || b[6] != 0 || b[7] != 34 {
		return nil, errors.New("sendspin: invalid FLAC STREAMINFO")
	}
	b[4] |= 0x80 // This is the final metadata block in our per-chunk decoder.
	s, err := flac.New(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if int(s.Info.SampleRate) != format.SampleRate || int(s.Info.NChannels) != format.Channels || int(s.Info.BitsPerSample) != format.BitDepth {
		return nil, errors.New("sendspin: FLAC header disagrees with negotiated format")
	}
	d.header = b
	return d, nil
}

// Decode returns interleaved float32, before volume or synchronization correction.
// One chunk must contain whole codec units and at most 150ms of decoded audio.
func (d *Decoder) Decode(data []byte) ([]float32, error) {
	f := d.format
	maxFrames := f.SampleRate * 150 / 1000
	if len(data) == 0 {
		return nil, errors.New("sendspin: empty audio chunk")
	}
	if f.Codec == "pcm" {
		width := f.BitDepth / 8
		if len(data)%(width*f.Channels) != 0 || len(data)/(width*f.Channels) > maxFrames {
			return nil, errors.New("sendspin: invalid PCM chunk length")
		}
		out := make([]float32, len(data)/width)
		for i := range out {
			if width == 2 {
				out[i] = float32(int16(binary.LittleEndian.Uint16(data[i*2:]))) / 32768
			} else {
				j := i * 3
				v := int32(data[j]) | int32(data[j+1])<<8 | int32(data[j+2])<<16
				out[i] = float32(v<<8>>8) / 8388608
			}
		}
		return out, nil
	}
	s, err := flac.New(io.MultiReader(bytes.NewReader(d.header), bytes.NewReader(data)))
	if err != nil {
		return nil, err
	}
	var out []float32
	scale := float32(int64(1) << (f.BitDepth - 1))
	for {
		frame, err := s.ParseNext()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("sendspin: decode FLAC: %w", err)
		}
		if len(frame.Subframes) != f.Channels || int(frame.SampleRate) != f.SampleRate || int(frame.BitsPerSample) != f.BitDepth {
			return nil, errors.New("sendspin: FLAC frame format mismatch")
		}
		n := len(frame.Subframes[0].Samples)
		if n == 0 || len(out)/f.Channels+n > maxFrames {
			return nil, errors.New("sendspin: FLAC chunk exceeds 150ms")
		}
		for _, sf := range frame.Subframes {
			if len(sf.Samples) != n {
				return nil, errors.New("sendspin: inconsistent FLAC channels")
			}
		}
		for i := 0; i < n; i++ {
			for _, sf := range frame.Subframes {
				out = append(out, float32(sf.Samples[i])/scale)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("sendspin: FLAC chunk contains no frames")
	}
	return out, nil
}
