package alsa

import (
	"encoding/hex"
	"testing"
)

func TestNativePCMEncodingEndpointsAndClipping(t *testing.T) {
	for _, test := range []struct {
		encoding sampleEncoding
		want     string
	}{
		{encodingFloat, "000080bf000080bf000000bf000000000000003f0000803f0000803f"},
		{encodingS32, "0000008000000080000000c00000000000000040ffffff7fffffff7f"},
		{encodingS24, "000080ff000080ff0000c0ff0000000000004000ffff7f00ffff7f00"},
		{encodingS24Packed, "0000800000800000c0000000000040ffff7fffff7f"},
		{encodingS16, "0080008000c000000040ff7fff7f"},
	} {
		got := hex.EncodeToString(encodeSamples([]float32{-2, -1, -.5, 0, .5, 1, 2}, test.encoding))
		if got != test.want {
			t.Errorf("encoding %d: %s != %s", test.encoding, got, test.want)
		}
	}
}
