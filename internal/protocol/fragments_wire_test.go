package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestFragmentSenderExactWireVectors(t *testing.T) {
	// Literal RC1 limits, independent of both Fragment's constants and Push.
	if MaxPlaintext != 65519 {
		t.Fatal("Noise limit minus 16-byte tag changed")
	}
	for _, id := range []byte{0, 4, 8, 16, 255} {
		for _, tc := range []struct {
			payload int
			lengths []int
			headers [][]byte
		}{
			{65519, []int{65519, 5}, [][]byte{{1, 2, id}, {1, 1}}},
			{131033, []int{65519, 65519}, [][]byte{{1, 2, id}, {1, 1}}},
			{131034, []int{65519, 65519, 3}, [][]byte{{1, 2, id}, {1, 0}, {1, 1}}},
		} {
			t.Run(fmt.Sprintf("id-%d/payload-%d", id, tc.payload), func(t *testing.T) {
				data := make([]byte, tc.payload)
				for i := range data {
					data[i] = byte(i*37 + 11)
				}
				message := append([]byte{id}, data...)
				original := bytes.Clone(message)
				var frames [][]byte
				if err := Fragment(message, func(frame []byte) error { frames = append(frames, bytes.Clone(frame)); return nil }); err != nil {
					t.Fatal(err)
				}
				if len(frames) != len(tc.lengths) || !bytes.Equal(message, original) {
					t.Fatal("wrong carrier count or sender mutated input")
				}
				offset := 0
				for i, frame := range frames {
					header := tc.headers[i]
					if len(frame) != tc.lengths[i] || len(frame)+16 > 65535 || !bytes.Equal(frame[:len(header)], header) {
						t.Fatal("wire header/flags/carrier length differs", i, len(frame), frame[:len(header)])
					}
					n := tc.lengths[i] - len(header)
					if !bytes.Equal(frame[len(header):], data[offset:offset+n]) {
						t.Fatal("wrong data boundary or repeated original ID", i)
					}
					offset += n
				}
				if offset != len(data) {
					t.Fatal("lost trailing payload")
				}
			})
		}
	}
}

func TestFragmentSenderNeverFragmentsFittingMessages(t *testing.T) {
	for _, size := range []int{1, 2, 65518, 65519} {
		for _, id := range []byte{0, 4, 255} {
			input := bytes.Repeat([]byte{0x7d}, size)
			input[0] = id
			calls := 0
			if err := Fragment(input, func(frame []byte) error {
				calls++
				if !bytes.Equal(frame, input) {
					t.Fatal("fitting message acquired fragment header or lost data", size, id)
				}
				return nil
			}); err != nil || calls != 1 {
				t.Fatal("fitting message did not use exactly one carrier", size, id, calls, err)
			}
		}
	}
}

func TestFragmentSenderRejectsRecursiveIDBeforeEmission(t *testing.T) {
	for _, input := range [][]byte{nil, {}, {1}, {1, 3, 0}, append([]byte{1}, make([]byte, 65519)...)} {
		calls := 0
		err := Fragment(input, func([]byte) error { calls++; return nil })
		if !errors.Is(err, ErrProtocol) || calls != 0 {
			t.Fatal("invalid input emitted a carrier", len(input), calls, err)
		}
	}
}

func TestFragmentSenderStopsAtFirstEmissionError(t *testing.T) {
	sentinel := errors.New("emission failed")
	for failAt := range 3 {
		calls := 0
		err := Fragment(append([]byte{0}, make([]byte, 131034)...), func([]byte) error {
			calls++
			if calls == failAt+1 {
				return sentinel
			}
			return nil
		})
		if !errors.Is(err, sentinel) || calls != failAt+1 {
			t.Fatal("sender continued after an emission failure", failAt, calls, err)
		}
	}
}
