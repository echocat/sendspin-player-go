package sendspin

import (
	"encoding/binary"

	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

type imageTransfer struct {
	channel  int
	expected int
	received int
	discard  bool
	image    *Artwork
}
type visualFrame struct {
	event Visualization
	size  int
}
type mediaState struct {
	pairingIndex    uint32
	artworkChannels []ArtworkChannel
	transfer        *imageTransfer
	images          [4]*Artwork // Protected by session.snapshotMu; the rest is actor-owned.
	pendingImages   [4]*Artwork
	visualizer      VisualizerConfig
	downbeats       bool
	visualLast      *int64
	visualQueue     []visualFrame
	visualBytes     int
}

func (s *session) mediaBinary(frame []byte) error {
	id := frame[0]
	if id >= 8 && id <= 11 {
		return s.artwork(frame)
	}
	if id >= 16 && id <= 20 {
		return s.visualization(frame)
	}
	return nil
}

func (s *session) artwork(frame []byte) error {
	if !s.has(ArtworkRole) || !s.streams["artwork"] || len(frame) < 2 || len(frame) > 65519 {
		return protocol.ErrProtocol
	}
	channel, flags := int(frame[0]-8), frame[1]
	if flags&0xfc != 0 || flags == 3 {
		return protocol.ErrProtocol
	}
	if channel >= len(s.media.artworkChannels) || s.media.artworkChannels[channel].Source == "none" {
		return protocol.ErrProtocol
	}
	switch flags {
	case 1:
		if len(frame) != 2 {
			return protocol.ErrProtocol
		}
		if s.media.transfer != nil && s.media.transfer.channel == channel {
			s.media.transfer = nil
		}
		s.media.pendingImages[channel] = nil
	case 2:
		if len(frame) != 14 || s.media.transfer != nil {
			return protocol.ErrProtocol
		}
		size := int64(binary.BigEndian.Uint32(frame[10:14]))
		if size > int64(s.client.cfg.MaxMessageBytes) {
			return protocol.ErrProtocol
		}
		image := &Artwork{Channel: channel, Timestamp: int64(binary.BigEndian.Uint64(frame[2:10]))}
		s.media.pendingImages[channel] = nil
		if size == 0 {
			if s.state.Available {
				s.media.pendingImages[channel] = image
			}
		} else {
			s.media.transfer = &imageTransfer{channel: channel, expected: int(size), image: image, discard: !s.state.Available}
		}
	case 0:
		t := s.media.transfer
		if t == nil || t.channel != channel || len(frame)-2 > t.expected-t.received {
			return protocol.ErrProtocol
		}
		t.received += len(frame) - 2
		if !s.state.Available {
			t.discard = true
			t.image.Data = nil
		}
		if !t.discard {
			t.image.Data = append(t.image.Data, frame[2:]...)
		}
		if t.received == t.expected {
			if !t.discard {
				s.media.pendingImages[channel] = t.image
			}
			s.media.transfer = nil
		}
	}
	return s.scheduled()
}

func (s *session) visualization(frame []byte) error {
	if !s.has(VisualizerRole) || !s.streams["visualizer"] || len(frame) < 9 {
		return protocol.ErrProtocol
	}
	typ := []string{"loudness", "beat", "f_peak", "spectrum", "peak"}[frame[0]-16]
	offered := false
	for _, t := range s.media.visualizer.Types {
		if t == typ {
			offered = true
		}
	}
	if !offered {
		return protocol.ErrProtocol
	}
	timestamp := int64(binary.BigEndian.Uint64(frame[1:9]))
	if s.media.visualLast != nil && timestamp < *s.media.visualLast {
		return protocol.ErrProtocol
	}
	s.media.visualLast = &timestamp
	v := Visualization{Type: typ, Timestamp: timestamp}
	switch typ {
	case "loudness":
		if len(frame) != 11 {
			return protocol.ErrProtocol
		}
		v.Values = []uint16{binary.BigEndian.Uint16(frame[9:])}
	case "beat":
		if len(frame) != 10 {
			return protocol.ErrProtocol
		}
		v.Downbeat = frame[9]&1 != 0
		if v.Downbeat && !s.media.downbeats {
			return protocol.ErrProtocol
		}
	case "f_peak":
		if len(frame) != 13 {
			return protocol.ErrProtocol
		}
		v.Values = []uint16{binary.BigEndian.Uint16(frame[9:]), binary.BigEndian.Uint16(frame[11:])}
		if v.Values[0] == 0 && v.Values[1] != 0 {
			return protocol.ErrProtocol
		}
	case "spectrum":
		if s.media.visualizer.Spectrum == nil || len(frame) != 9+2*s.media.visualizer.Spectrum.Bins {
			return protocol.ErrProtocol
		}
		for i := 9; i < len(frame); i += 2 {
			v.Values = append(v.Values, binary.BigEndian.Uint16(frame[i:]))
		}
	case "peak":
		if len(frame) != 10 {
			return protocol.ErrProtocol
		}
		v.Strength = frame[9]
	}
	if !s.state.Available {
		return nil
	}
	local, err := s.local(timestamp)
	if err != nil {
		return err
	}
	if local.Before(monotime.Now()) {
		return nil
	}
	if len(frame) > s.client.cfg.VisualizerCapacity-s.media.visualBytes {
		return protocol.ErrProtocol
	}
	s.media.visualBytes += len(frame)
	s.media.visualQueue = append(s.media.visualQueue, visualFrame{event: v, size: len(frame)})
	return nil
}

func (s *session) scheduled() error {
	now := monotime.Now()
	changed := false
	if s.pendingMetadata != nil {
		local, err := s.local(s.pendingMetadata.Timestamp)
		if err != nil {
			return err
		}
		if !local.After(now) {
			s.state.Metadata, s.pendingMetadata = s.pendingMetadata, nil
			changed = true
		}
	}
	if s.pendingColors != nil {
		local, err := s.local(s.pendingColors.Timestamp)
		if err != nil {
			return err
		}
		if !local.After(now) {
			s.state.Colors, s.pendingColors = s.pendingColors, nil
			changed = true
		}
	}
	for i, image := range s.media.pendingImages {
		if image == nil {
			continue
		}
		local, err := s.local(image.Timestamp)
		if err != nil {
			return err
		}
		if local.After(now) {
			continue
		}
		s.media.pendingImages[i] = nil
		if s.state.Available {
			s.applyArtwork(*image)
		}
	}
	for len(s.media.visualQueue) > 0 {
		local, err := s.local(s.media.visualQueue[0].event.Timestamp)
		if err != nil {
			return err
		}
		if local.After(now) {
			break
		}
		f := s.media.visualQueue[0]
		s.media.visualQueue[0] = visualFrame{}
		s.media.visualQueue = s.media.visualQueue[1:]
		s.media.visualBytes -= f.size
		if s.state.Available {
			s.client.emit(Event{Kind: "visualization", ServerID: s.channel.ServerID, Visualization: &f.event})
		}
	}
	if changed {
		s.publish()
	}
	return nil
}
