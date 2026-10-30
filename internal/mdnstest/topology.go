package mdnstest

import (
	"bytes"
	"errors"
	"io"
)

// Only the stdout reader invokes this writer. The fixture can request ordered
// attach/remove cycles of the runner's own internal network (also for -count).
// It never gets
// a daemon socket, host networking, extra capabilities or arbitrary commands.
type topologyWriter struct {
	destination io.Writer
	change      func(bool) error
	pending     []byte
	stage       int
	cycles      int
}

func (w *topologyWriter) Write(p []byte) (int, error) {
	n, err := w.destination.Write(p)
	if err != nil {
		return n, err
	}
	w.pending = append(w.pending, p...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		line := string(w.pending[:end])
		w.pending = w.pending[end+1:]
		switch line {
		case "[mdns-topology] attach":
			if w.stage != 0 {
				return n, errors.New("unexpected interface attach request")
			}
			if err = w.change(true); err != nil {
				return n, err
			}
			w.stage = 1
		case "[mdns-topology] detach":
			if w.stage != 1 {
				return n, errors.New("unexpected interface detach request")
			}
			if err = w.change(false); err != nil {
				return n, err
			}
			w.stage = 0
			w.cycles++
		}
	}
	if len(w.pending) > 256<<10 {
		return n, errors.New("oversized mDNS fixture output line")
	}
	return n, nil
}
