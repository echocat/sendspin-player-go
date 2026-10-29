package alsa

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"

	"github.com/echocat/sendspin-player-go/audio"
)

// Test-only input: exactly the offline generator's three-second PCM16 stereo
// container, left-only and bounded to the already approved weak reference level.
func loadWeakReference(path string) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	const size = 44 + 144000*4
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() != size {
		return nil, errors.New("reference must be exactly three seconds of generated 48kHz/stereo PCM16")
	}
	b, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return nil, err
	}
	if len(b) != size || string(b[:4]) != "RIFF" || binary.LittleEndian.Uint32(b[4:]) != size-8 || string(b[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(b[16:]) != 16 || binary.LittleEndian.Uint16(b[20:]) != 1 || binary.LittleEndian.Uint16(b[22:]) != 2 || binary.LittleEndian.Uint32(b[24:]) != 48000 || binary.LittleEndian.Uint32(b[28:]) != 192000 || binary.LittleEndian.Uint16(b[32:]) != 4 || binary.LittleEndian.Uint16(b[34:]) != 16 || string(b[36:40]) != "data" || binary.LittleEndian.Uint32(b[40:]) != size-44 {
		return nil, errors.New("reference is not the exact generated PCM16 WAV container")
	}
	samples := make([]float32, 144000*2)
	nonzero := false
	for i := range samples {
		v := int16(binary.LittleEndian.Uint16(b[44+i*2:]))
		if (i%2 == 1 && v != 0) || v < -328 || v > 328 {
			return nil, errors.New("reference must be left-only with peak no greater than 0.01 plus PCM16 rounding")
		}
		nonzero = nonzero || v != 0
		samples[i] = float32(v) / 32768
	}
	if !nonzero {
		return nil, errors.New("reference contains no signal")
	}
	// The final 200ms must remain silent so a bounded Flush cannot truncate a marker.
	if !bytes.Equal(b[len(b)-48000/5*4:], make([]byte, 48000/5*4)) {
		return nil, errors.New("reference lacks the required silent tail")
	}
	return samples, nil
}

func submitWeakReference(ctx context.Context, d audio.Device, samples []float32) (frames int, err error) {
	for len(samples) > 0 {
		if err = ctx.Err(); err != nil {
			return frames, err
		}
		if _, err = d.Next(ctx); err != nil {
			return frames, err
		}
		n := min(960, len(samples))
		if err = d.Write(ctx, samples[:n]); err != nil {
			return frames, err
		}
		frames += n / 2
		samples = samples[n:]
	}
	return frames, d.Flush(ctx)
}
