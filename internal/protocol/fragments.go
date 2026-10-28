package protocol

import "fmt"

const MaxPlaintext = 65519

// Reassembler tracks sequence even for unimplemented message IDs. Its limit is
// a local resource bound; it does not change Noise's per-message limit.
type Reassembler struct {
	Limit  int
	active bool
	kind   byte
	data   []byte
	size   int
	keep   bool
}

func (r *Reassembler) Push(frame []byte, known func(byte) bool) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, fmt.Errorf("%w: malformed fragment sequence", ErrProtocol) }
	if len(frame) == 0 || len(frame) > MaxPlaintext {
		return bad()
	}
	if frame[0] != 1 {
		if r.active {
			return bad()
		}
		return frame, nil
	}
	if len(frame) < 2 || frame[1]&0xfc != 0 {
		return bad()
	}
	first, last := frame[1]&2 != 0, frame[1]&1 != 0
	offset := 2
	if first {
		if r.active || len(frame) < 3 || frame[2] == 1 {
			return bad()
		}
		r.active, r.kind, r.keep = true, frame[2], known(frame[2])
		r.size, r.data = 0, nil
		offset = 3
	} else if !r.active {
		return bad()
	}
	n := len(frame) - offset
	if r.Limit <= 0 || n > r.Limit-r.size {
		return nil, fmt.Errorf("%w: reassembly resource limit", ErrProtocol)
	}
	r.size += n
	if r.keep {
		r.data = append(r.data, frame[offset:]...)
	}
	if !last {
		return nil, nil
	}
	r.active = false
	if !r.keep {
		return []byte{r.kind}, nil
	}
	result := append([]byte{r.kind}, r.data...)
	r.data = nil
	return result, nil
}

// Fragment emits full frames in order; callers must serialize the whole call.
func Fragment(message []byte, emit func([]byte) error) error {
	if len(message) == 0 || message[0] == 1 {
		return ErrProtocol
	}
	if len(message) <= MaxPlaintext {
		return emit(message)
	}
	kind, data, first := message[0], message[1:], true
	for len(data) > 0 {
		frame := []byte{1, 0}
		if first {
			frame[1] = 2
			frame = append(frame, kind)
			first = false
		}
		n := min(len(data), MaxPlaintext-len(frame))
		if n == len(data) {
			frame[1] |= 1
		}
		frame = append(frame, data[:n]...)
		if err := emit(frame); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
