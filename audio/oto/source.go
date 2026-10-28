package oto

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

// source never blocks Oto's shared reader: starvation produces silence. The
// bounded ring backpressures the SDK writer instead of buffering without limit.
type source struct {
	mu         sync.Mutex
	format     audio.Format
	ring       []byte
	head, size int
	closed     bool
	notify     chan struct{}
}

func newSource(format audio.Format) *source {
	return &source{format: format, ring: make([]byte, format.SampleRate/10*format.Channels*4), notify: make(chan struct{}, 1)}
}

func (s *source) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *source) Read(buf []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.EOF
	}
	clear(buf)
	n := min(len(buf), s.size)
	first := min(n, len(s.ring)-s.head)
	copy(buf, s.ring[s.head:s.head+first])
	copy(buf[first:n], s.ring[:n-first])
	s.head = (s.head + n) % len(s.ring)
	s.size -= n
	s.mu.Unlock()
	s.signal()
	return len(buf), nil
}

func (s *source) Seek(offset int64, whence int) (int64, error) {
	if offset != 0 || whence != io.SeekStart {
		return 0, errors.New("sendspin: Oto live source can only reset")
	}
	s.mu.Lock()
	s.head, s.size = 0, 0
	clear(s.ring)
	s.mu.Unlock()
	s.signal()
	return 0, nil
}

func (s *source) buffered() int { s.mu.Lock(); defer s.mu.Unlock(); return s.size }
func (s *source) close() {
	s.mu.Lock()
	s.closed = true
	s.size = 0
	clear(s.ring)
	s.mu.Unlock()
	s.signal()
}

func (s *source) write(ctx context.Context, samples []float32, status func() error) error {
	if len(samples)%s.format.Channels != 0 {
		return errors.New("sendspin: partial Oto output frame")
	}
	for len(samples) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if status != nil {
			if err := status(); err != nil {
				return err
			}
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errors.New("sendspin: Oto source closed")
		}
		n := min(len(samples)/s.format.Channels, (len(s.ring)-s.size)/(s.format.Channels*4)) * s.format.Channels
		tail := (s.head + s.size) % len(s.ring)
		for _, sample := range samples[:n] {
			binary.LittleEndian.PutUint32(s.ring[tail:tail+4], math.Float32bits(sample))
			tail = (tail + 4) % len(s.ring)
		}
		s.size += n * 4
		s.mu.Unlock()
		samples = samples[n:]
		if len(samples) > 0 {
			// A failed backend may stop reading forever. Check its error while
			// backpressured rather than relying on another reader notification.
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-s.notify:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return ctx.Err()
}
