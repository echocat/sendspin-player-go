package alsa

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
)

type sampleEncoding int

const (
	encodingFloat sampleEncoding = iota + 1
	encodingS32
	encodingS24
	encodingS24Packed
	encodingS16
)

func (e sampleEncoding) bytes() int {
	switch e {
	case encodingFloat, encodingS32, encodingS24:
		return 4
	case encodingS24Packed:
		return 3
	case encodingS16:
		return 2
	}
	return 0
}
func validateSamples(ctx context.Context, samples []float32) error {
	for i, value := range samples {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("alsa: nonfinite sample")
		}
	}
	return nil
}
func encodeSamples(samples []float32, e sampleEncoding) []byte {
	data := make([]byte, len(samples)*e.bytes())
	encodeInto(data, samples, e)
	return data
}

func encodeInto(data []byte, samples []float32, e sampleEncoding) {
	for i, sample := range samples {
		value := max(-1, min(1, float64(sample)))
		offset := i * e.bytes()
		switch e {
		case encodingFloat:
			binary.LittleEndian.PutUint32(data[offset:], math.Float32bits(float32(value)))
		case encodingS32:
			binary.LittleEndian.PutUint32(data[offset:], uint32(int32(max(-2147483648, min(2147483647, math.Round(value*2147483648))))))
		case encodingS24, encodingS24Packed:
			n := int32(max(-8388608, min(8388607, math.Round(value*8388608))))
			data[offset], data[offset+1], data[offset+2] = byte(n), byte(n>>8), byte(n>>16)
			if e == encodingS24 {
				data[offset+3] = byte(n >> 24)
			}
		case encodingS16:
			binary.LittleEndian.PutUint16(data[offset:], uint16(int16(max(-32768, min(32767, math.Round(value*32768))))))
		}
	}
}
