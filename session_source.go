package sendspin

import (
	"encoding/binary"
	"encoding/json"
	"slices"
	"time"

	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

type sourceState struct {
	supported bool
	pending   bool
	open      bool // client-stream/start was sent
	retiring  bool
	capture   *sourceCapture
	wake      chan struct{}
}

func (s *session) sourceHello(data []byte) error {
	if !slices.Contains(s.client.cfg.Roles, SourceRole) {
		return nil
	}
	var hello struct {
		Support json.RawMessage `json:"source@v1_support"`
	}
	if err := protocol.Decode(data, &hello, "source@v1_support"); err != nil {
		return err
	}
	var support struct {
		Codecs []string `json:"supported_codecs"`
	}
	if err := protocol.Decode(hello.Support, &support, "supported_codecs"); err != nil {
		return err
	}
	// Unknown codec identifiers are deliberately ignored; both baseline codecs
	// are required from a source-capable server, and we produce PCM16.
	if !slices.Contains(support.Codecs, "pcm") || !slices.Contains(support.Codecs, "flac") {
		return protocol.ErrProtocol
	}
	s.source.supported = true
	return nil
}

func (s *session) sourceAllowed() bool {
	return s.ctx.Err() == nil && s.has(SourceRole) && s.source.supported && s.activated && !s.awaitingActivation && s.state.Available && s.willing && s.filter.Ready() && (s.channel.Category == "lt" || s.client.cfg.AllowUnpaired)
}

func (s *session) sourceCommand(raw json.RawMessage) error {
	command, err := s.decodeSourceCommand(raw)
	if err != nil {
		return err
	}
	return s.applySourceCommand(command)
}

func (s *session) decodeSourceCommand(raw json.RawMessage) (string, error) {
	if !protocol.Object(raw) {
		return "", protocol.ErrProtocol
	}
	// Unsupported role bodies remain opaque for non-source clients.
	if !slices.Contains(s.client.cfg.Roles, SourceRole) {
		return "", nil
	}
	var command struct {
		Command string `json:"command"`
	}
	if err := protocol.Decode(raw, &command, "command"); err != nil {
		return "", err
	}
	if command.Command != "start" && command.Command != "stop" {
		return "", protocol.ErrProtocol
	}
	return command.Command, nil
}

func (s *session) applySourceCommand(command string) error {
	switch command {
	case "stop":
		return s.stopSource()
	case "start":
		if !s.sourceAllowed() || s.source.pending || s.source.open {
			return nil
		}
		s.source.pending = true // Exactly one authorization, including a late Open.
		s.launchSource()
		return nil
	default:
		return nil
	}
}

func (s *session) launchSource() {
	if !s.source.pending || s.source.capture != nil {
		return
	}
	if s.source.wake == nil {
		s.source.wake = make(chan struct{}, 1)
	}
	s.source.capture = newSourceCapture(s.ctx, s.client.cfg.Input, s.client.cfg.SourceFormat, s.source.wake)
	s.source.retiring = false
}

func (s *session) stopSource() error {
	s.source.pending = false
	if s.source.capture != nil {
		s.source.capture.cancel()
		s.source.retiring = true
	}
	if !s.source.open {
		return nil
	}
	s.source.open = false
	delete(s.streams, "source")
	s.publish()
	return s.send("client-stream/end", map[string]any{})
}

func (s *session) advanceSource() error {
	c := s.source.capture
	if c == nil || s.awaitingActivation {
		return nil
	}
	if s.source.retiring {
		select {
		case <-c.done:
			s.source.capture = nil
			s.source.retiring = false
			if s.sourceAllowed() {
				s.launchSource()
			} else {
				s.source.pending = false
			}
		default:
		}
		return nil
	}
	if !s.source.pending && !s.source.open {
		select {
		case <-c.done:
			s.source.capture = nil
		default:
		}
		return nil
	}
	if !s.sourceAllowed() {
		return s.stopSource()
	}
	if !s.source.open {
		select {
		case <-c.ready:
			f := s.client.cfg.SourceFormat
			if err := s.send("client-stream/start", map[string]any{"source": map[string]any{"codec": "pcm", "channels": f.Channels, "sample_rate": f.SampleRate, "bit_depth": 16}}); err != nil {
				return err
			}
			s.source.pending, s.source.open = false, true
			s.streams["source"] = true
			s.publish()
		default:
			select {
			case <-c.done:
				s.source.pending = false
				s.source.capture = nil
				s.sourceError(c)
			default:
			}
			return nil
		}
	}
	if chunk, ok := c.pop(); ok {
		// Bound app queue wait after stalls/rekey, not fixed ADC/pipeline latency.
		// The wire timestamp still uses the original first-sample ADC time.
		if monotime.Since(chunk.queuedAt) <= 100*time.Millisecond {
			at, ok := s.filter.CheckedServerTime(chunk.first.Sub(s.epoch).Microseconds() + 1)
			if !ok {
				return errClockMapping
			}
			frame := make([]byte, 9+len(chunk.pcm))
			frame[0] = 12
			binary.BigEndian.PutUint64(frame[1:9], uint64(at))
			copy(frame[9:], chunk.pcm)
			ctx, cancel := s.applicationWriteContext("source/audio")
			err := s.channel.Send(ctx, frame)
			cancel()
			if err != nil {
				return err
			}
		}
		return nil
	}
	select {
	case <-c.done:
		if err := s.stopSource(); err != nil {
			return err
		}
		s.source.capture = nil
		s.sourceError(c)
	default:
	}
	return nil
}

func (s *session) sourceError(c *sourceCapture) {
	// Errors are delivered to the application, never logged with backend details
	// (which may contain private device names). No samples enter normal logs.
	if c.err != nil {
		s.client.emit(Event{Kind: "source_error", ServerID: s.channel.ServerID, Err: c.err})
	}
}

func (s *session) closeSource() {
	if c := s.source.capture; c != nil {
		c.cancel()
		<-c.done // Input contract requires prompt cancellation, including Open.
	}
}
