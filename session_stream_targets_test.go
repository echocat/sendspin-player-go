package sendspin

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

type targetDecoder struct{ generation int }

func (*targetDecoder) Decode([]byte) ([]float32, error) { panic("unexpected decode") }

func targetSession(t *testing.T, active int) (*session, *int) {
	t.Helper()
	s := mediaSession(t)
	s.state.ActiveRoles = append(s.state.ActiveRoles, Player)
	s.streams = map[string]bool{}
	for i, family := range []string{"player", "artwork", "visualizer"} {
		if active&(1<<i) != 0 {
			s.streams[family] = true
		}
	}
	resets := new(int)
	s.client.cfg.Decoders["pcm"] = func(codec.Format, string) (codec.AudioDecoder, error) {
		*resets++
		return &targetDecoder{generation: *resets}, nil
	}
	s.streamFormat = codec.Format{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}
	s.decoder = &targetDecoder{}
	s.media.images[0] = &Artwork{Channel: 0, Data: []byte{1}}
	s.media.pendingImages[0] = &Artwork{Channel: 0, Timestamp: 99, Data: []byte{2}}
	s.media.visualQueue = []visualFrame{{event: Visualization{Type: "loudness", Timestamp: 100, Values: []uint16{123}}, size: 11}}
	s.media.visualBytes = 11
	cursor := int64(100)
	s.media.visualLast = &cursor
	return s, resets
}

func TestStreamClearAndEndTargetSelection(t *testing.T) {
	for _, kind := range []string{"stream/clear", "stream/end"} {
		for active := range 8 {
			for targets := -1; targets < 8; targets++ {
				t.Run(fmt.Sprintf("%s/active-%d/targets-%d", kind, active, targets), func(t *testing.T) {
					s, resets := targetSession(t, active)
					oldStreams := map[string]bool{}
					for k, v := range s.streams {
						oldStreams[k] = v
					}
					decoder, current, pending := s.decoder, s.media.images[0], s.media.pendingImages[0]
					queue := slices.Clone(s.media.visualQueue)
					cursor := s.media.visualLast
					payload := map[string]any{}
					allowed := 7
					if kind == "stream/clear" {
						allowed = 5 // Player and visualizer, never artwork.
						payload["server_transmitted"] = int64(-17)
					}
					selected := targets
					if targets < 0 {
						selected = active & allowed
					} else {
						roles := []string{}
						for i, family := range []string{"player", "artwork", "visualizer"} {
							if targets&(1<<i) != 0 {
								roles = append(roles, family)
							}
						}
						payload["roles"] = roles
					}
					valid := selected != 0 && selected&^active == 0 && selected&^allowed == 0
					err := s.dispatch(coreFrame(t, kind, payload))
					if !valid {
						if !errors.Is(err, protocol.ErrProtocol) || !reflect.DeepEqual(s.streams, oldStreams) || s.decoder != decoder || *resets != 0 || s.media.images[0] != current || s.media.pendingImages[0] != pending || !reflect.DeepEqual(s.media.visualQueue, queue) || s.media.visualBytes != 11 || s.media.visualLast != cursor {
							t.Fatal("invalid target selection mutated stream state", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					for i, family := range []string{"player", "artwork", "visualizer"} {
						wantActive := active&(1<<i) != 0 && (kind == "stream/clear" || selected&(1<<i) == 0)
						if s.streams[family] != wantActive {
							t.Fatal("wrong resulting stream lifetime", family, s.streams)
						}
					}
					if selected&1 != 0 {
						if kind == "stream/clear" {
							if *resets != 1 || s.decoder == decoder || s.decoder == nil {
								t.Fatal("player clear did not reset decoder while keeping stream")
							}
						} else if s.decoder != nil || *resets != 0 {
							t.Fatal("player end retained/reset rather than released decoder")
						}
					} else if s.decoder != decoder || *resets != 0 {
						t.Fatal("untargeted player changed")
					}
					if selected&2 != 0 {
						if s.media.images[0] != nil || s.media.pendingImages[0] != nil {
							t.Fatal("artwork end retained displayed/pending data")
						}
					} else if s.media.images[0] != current || s.media.pendingImages[0] != pending {
						t.Fatal("untargeted artwork changed")
					}
					if selected&4 != 0 {
						if len(s.media.visualQueue) != 0 || s.media.visualBytes != 0 || s.media.visualLast != nil {
							t.Fatal("visualizer target retained queue/count/order")
						}
					} else if !reflect.DeepEqual(s.media.visualQueue, queue) || s.media.visualBytes != 11 || s.media.visualLast != cursor {
						t.Fatal("untargeted visualizer changed")
					}
				})
			}
		}
	}
}

func TestStreamTargetGrammarRejectsBeforeMutation(t *testing.T) {
	for _, kind := range []string{"stream/clear", "stream/end"} {
		for _, roles := range []any{nil, false, 3, "player", map[string]any{}, []any{nil}, []any{1}, []string{"player", "player"}, []string{"visualizer", "player", "visualizer"}, []string{"Player"}, []string{"player@v1"}, []string{"metadata"}, []string{"source"}, []string{"_custom"}} {
			s, resets := targetSession(t, 7)
			decoder := s.decoder
			payload := map[string]any{"roles": roles}
			if kind == "stream/clear" {
				payload["server_transmitted"] = 0
			}
			if err := s.dispatch(coreFrame(t, kind, payload)); !errors.Is(err, protocol.ErrProtocol) || len(s.streams) != 3 || s.decoder != decoder || *resets != 0 || s.media.visualBytes != 11 || s.media.images[0] == nil {
				t.Fatal("malformed target list mutated output", kind, roles, err)
			}
		}
	}
	for _, bad := range []any{nil, false, "0", 1.5, []any{}, map[string]any{}} {
		s, resets := targetSession(t, 7)
		if err := s.dispatch(coreFrame(t, "stream/clear", map[string]any{"server_transmitted": bad})); !errors.Is(err, protocol.ErrProtocol) || *resets != 0 || len(s.streams) != 3 {
			t.Fatal("clear accepted malformed required timestamp", bad, err)
		}
	}
	s, resets := targetSession(t, 7)
	if err := s.dispatch(coreFrame(t, "stream/clear", map[string]any{})); !errors.Is(err, protocol.ErrProtocol) || *resets != 0 {
		t.Fatal("clear accepted missing timestamp", err)
	}
}

func TestStreamEndIgnoresUnknownTimestampField(t *testing.T) {
	// server_transmitted belongs to start/clear, not end. Its body is an unknown
	// end field and cannot acquire type requirements from a shared Go model.
	for _, value := range []any{nil, false, "unused", []any{nil}, map[string]any{"future": nil}} {
		s, _ := targetSession(t, 7)
		if err := s.dispatch(coreFrame(t, "stream/end", map[string]any{"roles": []string{"visualizer"}, "server_transmitted": value})); err != nil || s.streams["visualizer"] || !s.streams["player"] || !s.streams["artwork"] {
			t.Fatal("unknown end field was validated as a clear timestamp", value, err)
		}
	}
}

func TestStreamEndChecksInFlightArtworkBeforeOtherTargets(t *testing.T) {
	for _, roles := range [][]string{{"player", "artwork", "visualizer"}, {"visualizer", "artwork", "player"}, {"artwork", "player", "visualizer"}} {
		s, resets := targetSession(t, 7)
		transfer := &imageTransfer{channel: 0, expected: 3, received: 1}
		s.media.transfer = transfer
		decoder := s.decoder
		if err := s.dispatch(coreFrame(t, "stream/end", map[string]any{"roles": roles})); !errors.Is(err, protocol.ErrProtocol) || len(s.streams) != 3 || s.decoder != decoder || *resets != 0 || s.media.visualBytes != 11 || s.media.transfer != transfer {
			t.Fatal("uncancelled artwork end partially ended another role", roles, err)
		}
	}
}
