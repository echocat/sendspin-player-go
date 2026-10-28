package codec

import (
	"errors"

	"github.com/thesyncim/gopus"
)

type opusDecoder struct {
	decoder  *gopus.Decoder
	samples  []float32
	channels int
}

func newOpus(f Format, header string) (AudioDecoder, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if header != "" {
		return nil, errors.New("sendspin: Opus packets must not carry a codec header")
	}
	cfg := gopus.DefaultDecoderConfig(f.SampleRate, f.Channels)
	cfg.MaxPacketSamples = f.SampleRate * 120 / 1000
	cfg.MaxPacketBytes = 65518
	d, err := gopus.NewDecoder(cfg)
	if err != nil {
		return nil, err
	}
	return &opusDecoder{decoder: d, samples: make([]float32, cfg.MaxPacketSamples*f.Channels), channels: f.Channels}, nil
}
func (d *opusDecoder) Decode(packet []byte) ([]float32, error) {
	if len(packet) == 0 {
		return nil, errors.New("sendspin: empty Opus packet (PLC is not a received codec unit)")
	}
	n, err := d.decoder.Decode(packet, d.samples)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n*d.channels > len(d.samples) {
		return nil, errors.New("sendspin: invalid Opus packet duration")
	}
	return append([]float32(nil), d.samples[:n*d.channels]...), nil
}
