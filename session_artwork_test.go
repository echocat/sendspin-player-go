package sendspin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func artworkAnnounce(channel byte, timestamp int64, size uint32) []byte {
	frame := binary.BigEndian.AppendUint64([]byte{8 + channel, 2}, uint64(timestamp))
	return binary.BigEndian.AppendUint32(frame, size)
}

func artworkTestSession(t *testing.T) *session {
	t.Helper()
	s := mediaSession(t)
	s.client.active = s
	s.streams["artwork"] = true
	s.media.artworkChannels = slices.Clone(s.client.cfg.Artwork)
	return s
}

func TestArtworkMalformedWireAndSequenceMatrix(t *testing.T) {
	for _, frames := range [][][]byte{
		{{}}, {{8}}, {{8, 0}}, {{8, 1, 0}}, {{8, 3}}, {{8, 4}}, {{8, 0x80}},
		{artworkAnnounce(0, 0, 1)[:13]}, {append(artworkAnnounce(0, 0, 1), 0)},
		{artworkAnnounce(0, 0, 4), artworkAnnounce(0, 0, 0)},
		{artworkAnnounce(0, 0, 4), artworkAnnounce(1, 0, 1)},
		{artworkAnnounce(0, 0, 4), {9, 0, 1}},
		{artworkAnnounce(0, 0, 1), {8, 0, 1, 2}},
		{artworkAnnounce(0, 0, 1), {8, 0, 1}, {8, 0, 2}},
		{artworkAnnounce(0, 0, 65518), append([]byte{8, 0}, make([]byte, 65518)...)},
	} {
		for _, available := range []bool{false, true} {
			s := artworkTestSession(t)
			s.state.Available = available
			s.media.artworkChannels = append(s.media.artworkChannels, ArtworkChannel{Source: "artist", Format: "png", Width: 20, Height: 20})
			failed := false
			for _, frame := range frames {
				if err := s.artwork(frame); err != nil {
					if !errors.Is(err, protocol.ErrProtocol) {
						t.Fatal(err)
					}
					failed = true
					break
				}
			}
			if !failed {
				t.Fatal("malformed artwork accepted", available, frames)
			}
		}
	}
}

func TestArtworkCancellationIsPerChannelAndPreservesCurrent(t *testing.T) {
	s := artworkTestSession(t)
	s.media.artworkChannels = append(s.media.artworkChannels, ArtworkChannel{Source: "artist", Format: "jpeg", Width: 20, Height: 20})
	s.applyArtwork(Artwork{Channel: 0, Timestamp: 1, Data: []byte{7, 7}})
	s.applyArtwork(Artwork{Channel: 1, Timestamp: 1, Data: []byte{8, 8}})
	s.media.pendingImages[1] = &Artwork{Channel: 1, Timestamp: s.now() + 10_000_000, Data: []byte{9, 9}}
	if err := s.artwork(artworkAnnounce(0, 0, 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{8, 0}); err != nil {
		t.Fatal("zero-byte part is not forbidden by RC1", err)
	}
	if err := s.artwork([]byte{8, 0, 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{9, 1}); err != nil {
		t.Fatal(err)
	}
	if s.media.transfer == nil || s.media.transfer.channel != 0 || s.media.transfer.received != 1 || s.media.pendingImages[1] != nil {
		t.Fatal("other-channel cancel touched transfer or retained pending image")
	}
	if image, ok := s.client.CurrentArtwork(0); !ok || !bytes.Equal(image.Data, []byte{7, 7}) {
		t.Fatal("partial transfer became current")
	}
	if err := s.serverState([]byte(`{"metadata":{"timestamp":0,"title":"interleaved"}}`)); err != nil {
		t.Fatal("unrelated message rejected within transfer", err)
	}
	if err := s.artwork([]byte{8, 1}); err != nil {
		t.Fatal(err)
	}
	if s.media.transfer != nil {
		t.Fatal("same-channel cancel retained partial bytes")
	}
	if image, ok := s.client.CurrentArtwork(0); !ok || !bytes.Equal(image.Data, []byte{7, 7}) {
		t.Fatal("cancel changed current image")
	}
	if err := s.artwork(artworkAnnounce(1, 0, 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{9, 0, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if image, ok := s.client.CurrentArtwork(1); !ok || !bytes.Equal(image.Data, []byte{5, 6}) {
		t.Fatal("next-channel image was not applied")
	}
}

func TestArtworkStreamEndRequiresCancelButIdenticalStartPreservesTransfer(t *testing.T) {
	s := artworkTestSession(t)
	if err := s.artwork(artworkAnnounce(0, 0, 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{8, 0, 1}); err != nil {
		t.Fatal(err)
	}
	transfer := s.media.transfer
	if err := s.startStream([]byte(`{"server_transmitted":0,"artwork":{"channels":[{"source":"album","format":"png","width":20,"height":20}]}}`)); err != nil || s.media.transfer != transfer {
		t.Fatal("identical configuration reset transfer", err)
	}
	if err := s.stopStream("stream/end", []byte(`{"roles":["artwork"]}`)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("uncancelled transfer survived stream/end", err)
	}
	if err := s.artwork([]byte{8, 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.stopStream("stream/end", []byte(`{"roles":["artwork"]}`)); err != nil {
		t.Fatal(err)
	}
	if s.streams["artwork"] || s.media.transfer != nil {
		t.Fatal("ended stream retained transfer")
	}
	if err := s.artwork(artworkAnnounce(0, 0, 0)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("artwork accepted outside stream", err)
	}
}

func TestArtworkMessageBoundaryAndEmptyTruncatedDeclaration(t *testing.T) {
	s := artworkTestSession(t)
	if err := s.artwork(artworkAnnounce(0, 0, 65517)); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork(append([]byte{8, 0}, bytes.Repeat([]byte{3}, 65517)...)); err != nil {
		t.Fatal("65519-byte message rejected", err)
	}
	if image, ok := s.client.CurrentArtwork(0); !ok || len(image.Data) != 65517 {
		t.Fatal("maximum single part incomplete")
	}
	s = artworkTestSession(t)
	s.streams["artwork"] = false
	s.client.cfg.Artwork = []ArtworkChannel{{Source: "none"}}
	s.media.transfer = nil
	if err := s.startStream([]byte(`{"server_transmitted":0,"artwork":{"channels":[]}}`)); err != nil {
		t.Fatal("all-disabled server declaration may be empty", err)
	}
	if err := s.artwork([]byte{8, 1}); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("disabled channel accepted artwork", err)
	}
}

func TestArtworkStreamDeclarationAndResourceBounds(t *testing.T) {
	for _, channels := range []string{
		`null`, `{}`, `[]`, `[{}]`,
		`[{"source":"album","format":"png","width":20}]`,
		`[{"source":"album","format":"png","width":21,"height":20}]`,
		`[{"source":"album","format":"jpeg","width":20,"height":20}]`,
		`[{"source":"none"}]`,
		`[{"source":"album","format":"png","width":20,"height":20},{"source":"none"},{"source":"none"},{"source":"none"},{"source":"none"}]`,
	} {
		s := artworkTestSession(t)
		s.streams["artwork"] = false
		if err := s.startStream([]byte(`{"server_transmitted":0,"artwork":{"channels":` + channels + `}}`)); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("nonmatching artwork declaration accepted", channels, err)
		}
		if s.streams["artwork"] {
			t.Fatal("rejected declaration opened a stream")
		}
	}
	s := artworkTestSession(t)
	s.client.cfg.MaxMessageBytes = 65519
	for _, available := range []bool{true, false} {
		s.state.Available = available
		if err := s.artwork(artworkAnnounce(0, 0, ^uint32(0))); !errors.Is(err, protocol.ErrProtocol) || s.media.transfer != nil {
			t.Fatal("oversized allocation accepted", available, err)
		}
	}
	// The announce alone must not allocate its declared payload in advance.
	s.state.Available = true
	if err := s.artwork(artworkAnnounce(0, 0, 65519)); err != nil {
		t.Fatal(err)
	}
	if s.media.transfer == nil || len(s.media.transfer.image.Data) != 0 || cap(s.media.transfer.image.Data) != 0 {
		t.Fatal("announce eagerly allocated image data")
	}
}

func TestArtworkPendingUsesBestEstimateBeforeClockConvergence(t *testing.T) {
	s := artworkTestSession(t)
	s.applyArtwork(Artwork{Channel: 0, Timestamp: 1, Data: []byte{7}})
	future := s.now() + 5_000_000
	if err := s.artwork(artworkAnnounce(0, future, 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{8, 0, 1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if s.media.pendingImages[0] == nil || s.media.transfer != nil {
		t.Fatal("complete future image not retained as pending")
	}
	if image, ok := s.client.CurrentArtwork(0); !ok || !bytes.Equal(image.Data, []byte{7}) {
		t.Fatal("future image applied early")
	}
	s.filter.Update(future+1_000_000, 1, s.now()+1)
	if s.filter.Ready() {
		t.Fatal("test prematurely converged")
	}
	if err := s.scheduled(); err != nil {
		t.Fatal(err)
	}
	if image, ok := s.client.CurrentArtwork(0); !ok || image.Timestamp != future || !bytes.Equal(image.Data, []byte{1, 2, 3}) || s.media.pendingImages[0] != nil {
		t.Fatal("complete artwork waited for convergence or was dropped for lateness")
	}
}
