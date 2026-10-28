package sendspin

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/wire"
)

func FuzzRoleBinaryMessages(f *testing.F) {
	announce := make([]byte, 14)
	announce[0] = 8
	announce[1] = 2
	binary.BigEndian.PutUint32(announce[10:], 3)
	seed := append([]byte{14}, announce...)
	seed = append(seed, 5, 8, 0, 1, 2, 3)
	f.Add(seed)
	cancelSeed := append([]byte(nil), seed[:15]...)
	cancelSeed = append(cancelSeed, 3, 8, 0, 1, 2, 8, 1)
	cancelSeed = append(cancelSeed, 14)
	cancelSeed = append(cancelSeed, artworkAnnounce(1, 0, 2)...)
	cancelSeed = append(cancelSeed, 4, 9, 0, 4, 5)
	f.Add(cancelSeed)
	f.Add([]byte{11, 16, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			return
		}
		client := &Client{cfg: Config{MaxMessageBytes: 65519, VisualizerCapacity: 32768}, events: make(chan Event, 16)}
		s := newSession(client, context.Background(), func() {})
		s.channel = &wire.Channel{ServerID: "fuzz"}
		s.state.ActiveRoles = []Role{ArtworkRole, VisualizerRole}
		s.streams["artwork"] = true
		s.streams["visualizer"] = true
		for range 4 {
			s.media.artworkChannels = append(s.media.artworkChannels, ArtworkChannel{Source: "album", Format: "png", Width: 2, Height: 2})
		}
		s.media.visualizer = VisualizerConfig{Types: []string{"loudness", "beat", "f_peak", "spectrum", "peak"}, RateMax: 30, Spectrum: &Spectrum{Bins: 4}}
		s.media.downbeats = true
		for len(data) > 0 {
			length := min(int(data[0]), len(data)-1)
			data = data[1:]
			if length == 0 {
				continue
			}
			frame := data[:length]
			data = data[length:]
			s.state.Available = (len(data)+int(frame[0]))&1 == 0
			if err := s.mediaBinary(frame); err != nil {
				return
			}
			if transfer := s.media.transfer; transfer != nil {
				if transfer.received > transfer.expected || transfer.received < 0 || transfer.expected > client.cfg.MaxMessageBytes {
					t.Fatal("invalid transfer accounting")
				}
				if transfer.discard && len(transfer.image.Data) > 0 {
					t.Fatal("discarded transfer retained image data")
				}
				if !transfer.discard && len(transfer.image.Data) != transfer.received {
					t.Fatal("retained transfer lost/duplicated part bytes")
				}
				if s.media.pendingImages[transfer.channel] != nil {
					t.Fatal("one channel retained both partial and complete pending images")
				}
			}
			for _, images := range [][4]*Artwork{s.media.images, s.media.pendingImages} {
				for _, image := range images {
					if image != nil && len(image.Data) > client.cfg.MaxMessageBytes {
						t.Fatal("encoded image exceeded configured limit")
					}
				}
			}
			visualBytes := 0
			for i, frame := range s.media.visualQueue {
				visualBytes += frame.size
				if i > 0 && frame.event.Timestamp < s.media.visualQueue[i-1].event.Timestamp {
					t.Fatal("visualizer queue lost timestamp order")
				}
			}
			if s.media.visualBytes != visualBytes || visualBytes < 0 || visualBytes > client.cfg.VisualizerCapacity {
				t.Fatal("visualizer lost byte accounting or exceeded its cap")
			}
		}
	})
}
