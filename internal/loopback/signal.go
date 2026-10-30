package loopback

import (
	"encoding/hex"
	"errors"
	"math"
)

const markerFrames = 2048
const chipFrames = 8

type marker struct {
	ID     int `json:"id"`
	Start  int `json:"start_frame"`
	Frames int `json:"frames"`
}
type manifest struct {
	Version   int      `json:"version"`
	Kind      string   `json:"kind"`
	Rate      int      `json:"sample_rate"`
	Frames    int      `json:"frames"`
	Channels  int      `json:"channels"`
	Channel   int      `json:"reference_channel"`
	SHA256    string   `json:"reference_sha256"`
	Seed      uint64   `json:"seed"`
	Amplitude float64  `json:"peak_amplitude"`
	Markers   []marker `json:"markers"`
}

// SplitMix64 is a deterministic test-signal generator, not cryptographic entropy.
func nextRandom(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	v := *state
	v = (v ^ (v >> 30)) * 0xbf58476d1ce4e5b9
	v = (v ^ (v >> 27)) * 0x94d049bb133111eb
	return v ^ (v >> 31)
}

func referenceSignal(rate, seconds, channels, channel int, amplitude float64, seed uint64) ([]float32, manifest, error) {
	m := manifest{Version: 1, Kind: "offline-reference-wave", Rate: rate, Frames: rate * seconds, Channels: channels, Channel: channel, Seed: seed, Amplitude: amplitude}
	if rate < 8000 || rate > 192000 || seconds < 3 || seconds > 120 || m.Frames > maxFrames || channels < 1 || channels > 8 || channel < 0 || channel >= channels || math.IsNaN(amplitude) || math.IsInf(amplitude, 0) || amplitude <= 0 || amplitude > .1 {
		return nil, m, errors.New("reference requires 8..192kHz, 3..120s, 1..8 channels, valid channel and amplitude (0,0.1]")
	}
	out := make([]float32, m.Frames)
	state := seed
	for at := rate / 2; at+markerFrames <= len(out)-rate/5; at += rate {
		pulse := make([]float64, markerFrames+2)
		for i := 0; i < markerFrames; i += chipFrames {
			v := 1.0
			if nextRandom(&state)&1 == 0 {
				v = -1
			}
			for j := i; j < min(i+chipFrames, markerFrames); j++ {
				pulse[j+1] = v
			}
		}
		for i := range markerFrames {
			window := .5 - .5*math.Cos(2*math.Pi*float64(i)/float64(markerFrames-1))
			out[at+i] = float32(amplitude * window * (pulse[i] + 2*pulse[i+1] + pulse[i+2]) / 4)
		}
		m.Markers = append(m.Markers, marker{ID: len(m.Markers), Start: at, Frames: markerFrames})
	}
	if len(m.Markers) < 3 {
		return nil, m, errors.New("reference too short for three markers")
	}
	return out, m, nil
}

func (m manifest) validate(w wave) error {
	if w.Rate < 8000 || w.Rate > 192000 || w.Channels < 1 || w.Channels > 8 || w.Channel < 0 || w.Channel >= w.Channels || len(w.Samples) == 0 || len(w.Samples) > maxFrames || math.IsNaN(m.Amplitude) || math.IsInf(m.Amplitude, 0) || m.Amplitude <= 0 || m.Amplitude > .1 {
		return errors.New("invalid reference metadata")
	}
	if sum, err := hex.DecodeString(m.SHA256); err != nil || len(sum) != 32 {
		return errors.New("invalid reference hash")
	}
	if m.Version != 1 || m.Kind != "offline-reference-wave" || m.Rate != w.Rate || m.Channels != w.Channels || m.Channel != w.Channel || m.Frames != len(w.Samples) || m.SHA256 != w.SHA256 || len(m.Markers) < 3 || len(m.Markers) > 120 {
		return errors.New("manifest/reference provenance or format mismatch")
	}
	previousEnd := 0
	for i, p := range m.Markers {
		if p.ID != i || p.Start < previousEnd || p.Frames != markerFrames || p.Start < 0 || p.Start > len(w.Samples)-p.Frames {
			return errors.New("invalid reference marker bounds/order")
		}
		previousEnd = p.Start + p.Frames
	}
	return nil
}
