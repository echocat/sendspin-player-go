package sendspin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

// unusedOutput lets protocol-only tests construct a player without hardware.
type unusedOutput struct{}

func (unusedOutput) Open(context.Context, audio.Format) (audio.Device, error) {
	return nil, errors.New("unexpected output open")
}

type unusedDecoder struct{}

func (unusedDecoder) Decode([]byte) ([]float32, error) { return nil, errors.New("unexpected decode") }

func TestStreamStartCodecHeaderPresence(t *testing.T) {
	for _, name := range []string{"pcm", "opus", "flac"} {
		for _, test := range []struct {
			field   string
			present bool
			null    bool
		}{
			{"", false, false}, {`,"codec_header":""`, true, false}, {`,"codec_header":"ZkxhQw=="`, true, false}, {`,"codec_header":null`, true, true},
		} {
			t.Run(name+test.field, func(t *testing.T) {
				called := false
				factory := func(codec.Format, string) (codec.AudioDecoder, error) { called = true; return unusedDecoder{}, nil }
				format := codec.Format{Codec: name, SampleRate: 48000, Channels: 1, BitDepth: 16}
				formats := []codec.Format{format}
				if name == "opus" {
					formats = append(formats, codec.Format{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16})
				}
				client, err := New(context.Background(), Config{Name: "headers", Store: &MemoryStore{}, Roles: []Role{Player}, Output: unusedOutput{}, Formats: formats, Decoders: map[string]codec.Factory{name: factory}})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := newSession(client, ctx, cancel)
				s.channel = &wire.Channel{}
				s.state.ActiveRoles = []Role{Player}
				err = s.startStream([]byte(`{"server_transmitted":0,"player":{"codec":"` + name + `","sample_rate":48000,"channels":1,"bit_depth":16` + test.field + `}}`))
				valid := (test.present == (name == "flac")) && !test.null
				if valid {
					if err != nil || !called || !s.streams["player"] {
						t.Fatalf("structurally valid header did not reach decoder: %v", err)
					}
				} else if !errors.Is(err, protocol.ErrProtocol) || called || s.streams["player"] {
					t.Fatalf("invalid presence reached decoder: called=%v, err=%v", called, err)
				}
			})
		}
	}
}

func TestStreamStartCodecHeaderBase64IsCheckedBeforeCustomFactory(t *testing.T) {
	for _, value := range []string{"ZkxhQw==", "", "+/8=", "Zg==", "ZkxhQw", "ZkxhQw===", "Zkxh\nQw==", "\rZkxhQw==", "ZkxhQw==\r\n", "-_8=", "Zh=="} {
		called := false
		format := codec.Format{Codec: "flac", SampleRate: 48000, Channels: 1, BitDepth: 16}
		factory := func(codec.Format, string) (codec.AudioDecoder, error) { called = true; return unusedDecoder{}, nil }
		client, err := New(context.Background(), Config{Name: "Header encoding", Store: &MemoryStore{}, Roles: []Role{Player}, Output: unusedOutput{}, Formats: []codec.Format{format}, Decoders: map[string]codec.Factory{"flac": factory}})
		if err != nil {
			t.Fatal(err)
		}
		s := newSession(client, context.Background(), func() {})
		s.channel = &wire.Channel{}
		s.state.ActiveRoles = []Role{Player}
		data, err := json.Marshal(map[string]any{"server_transmitted": 0, "player": map[string]any{"codec": "flac", "sample_rate": 48000, "channels": 1, "bit_depth": 16, "codec_header": value}})
		if err != nil {
			t.Fatal(err)
		}
		err = s.startStream(data)
		canonical := value == "ZkxhQw==" || value == "" || value == "+/8=" || value == "Zg=="
		if canonical {
			// Only lexical Base64 is a core check. Content remains this custom
			// factory's responsibility; built-ins require real STREAMINFO.
			if err != nil || !called || !s.streams["player"] {
				t.Fatal("canonical header did not reach custom factory", err)
			}
		} else if !errors.Is(err, protocol.ErrProtocol) || called || s.streams["player"] {
			t.Fatal("bad Base64 reached custom decoder or mutated stream", called, err)
		}
	}
}

func mediaSession(t *testing.T) *session {
	t.Helper()
	c, err := New(context.Background(), Config{Name: "roles", Store: &MemoryStore{}, Roles: []Role{MetadataRole, ColorRole, ArtworkRole, VisualizerRole}, Artwork: []ArtworkChannel{{Source: "album", Format: "png", Width: 20, Height: 20}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := newSession(c, ctx, cancel)
	s.channel = &wire.Channel{ServerID: "test"}
	s.hello = true
	s.activated = true
	s.admitted = true
	s.state.Available = true
	s.state.ActiveRoles = c.cfg.Roles
	return s
}

func TestUnknownTypesIgnoredOnlyAfterActivation(t *testing.T) {
	for _, kind := range []string{"", "future", "  ", "future/message"} {
		body, _ := json.Marshal(map[string]any{"type": kind, "payload": map[string]any{"future": nil}})
		frame := append([]byte{0}, body...)
		for _, phase := range []string{"before-hello", "before-activation", "active", "rehandshake"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				s := mediaSession(t)
				s.hello = phase != "before-hello"
				s.activated = phase == "active" || phase == "rehandshake"
				s.awaitingActivation = phase == "rehandshake"
				err := s.dispatch(frame)
				if phase == "active" {
					if err != nil {
						t.Fatal("valid extension rejected", err)
					}
				} else if !errors.Is(err, protocol.ErrProtocol) {
					t.Fatal("ignore rule bypassed activation gate", err)
				}
			})
		}
	}
}
func TestScheduledMetadataReplacementAndRoleRemoval(t *testing.T) {
	s := mediaSession(t)
	if err := s.serverState([]byte(`{"metadata":{"timestamp":0,"title":"current","progress":{"track_progress":100,"track_duration":500,"playback_speed":1000}}}`)); err != nil {
		t.Fatal(err)
	}
	future := s.now() + 1000000
	b, _ := json.Marshal(map[string]any{"metadata": Metadata{Timestamp: future, Title: "next"}})
	if err := s.serverState(b); err != nil {
		t.Fatal(err)
	}
	if s.state.Metadata.Title != "current" || s.pendingMetadata.Title != "next" {
		t.Fatal("future update applied early")
	}
	if err := s.serverState([]byte(`{"metadata":{"timestamp":0,"title":"replacement"}}`)); err != nil {
		t.Fatal(err)
	}
	if s.pendingMetadata != nil || s.state.Metadata.Title != "replacement" || s.state.Metadata.Progress != nil {
		t.Fatal("replacement retained partial state")
	}
	s.clearRole("metadata")
	if s.state.Metadata != nil || s.pendingMetadata != nil {
		t.Fatal("role state retained")
	}
}
func TestArtworkTransferAndMalformedSequences(t *testing.T) {
	s := mediaSession(t)
	s.streams["artwork"] = true
	s.media.artworkChannels = s.client.cfg.Artwork
	announce := make([]byte, 14)
	announce[0] = 8
	announce[1] = 2
	binary.BigEndian.PutUint32(announce[10:], 3)
	if err := s.artwork(announce); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork(announce); err == nil {
		t.Fatal("accepted overlapping announce")
	}
	if err := s.artwork([]byte{8, 0, 1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if s.media.images[0] == nil || len(s.media.images[0].Data) != 3 {
		t.Fatal("did not apply complete image")
	}
	if err := s.artwork([]byte{8, 0, 4}); err == nil {
		t.Fatal("accepted orphan part")
	}
	if err := s.artwork([]byte{8, 3}); err == nil {
		t.Fatal("accepted flags")
	}
	if err := s.artwork([]byte{8, 1}); err != nil {
		t.Fatal(err)
	}
}
func TestVisualizerValidation(t *testing.T) {
	s := mediaSession(t)
	s.streams["visualizer"] = true
	s.media.visualizer = VisualizerConfig{Types: []string{"loudness"}, RateMax: 30}
	frame := make([]byte, 11)
	frame[0] = 16
	binary.BigEndian.PutUint64(frame[1:], uint64(s.now()+1000000))
	binary.BigEndian.PutUint16(frame[9:], 123)
	if err := s.visualization(frame); err != nil {
		t.Fatal(err)
	}
	if len(s.media.visualQueue) != 1 {
		t.Fatal("future frame lost")
	}
	stale := s.now() - 1_000_000
	binary.BigEndian.PutUint64(frame[1:], uint64(stale))
	if err := s.visualization(frame); err == nil {
		t.Fatal("accepted decreasing timestamp")
	}
	s.media.visualLast = nil
	s.media.visualQueue = nil
	s.media.visualBytes = 0
	if err := s.visualization(frame); err != nil {
		t.Fatal(err)
	}
	if len(s.media.visualQueue) != 0 {
		t.Fatal("rendered stale visualizer")
	}
}
func TestControllerArgumentsAndProgress(t *testing.T) {
	max := int64(500)
	v := 0
	mute := false
	position := int64(0)
	for _, c := range []Command{{Command: "play"}, {Command: "volume", Volume: &v}, {Command: "mute", Mute: &mute}, {Command: "seek", PositionMS: &position}} {
		if !validCommand(c, &max) {
			t.Fatal("rejected valid zero value", c)
		}
	}
	for _, c := range []Command{{Command: "seek"}, {Command: "volume"}, {Command: "play", Volume: &v}, {Command: "future"}} {
		if validCommand(c, &max) {
			t.Fatal("accepted invalid command", c)
		}
	}
	m := Metadata{Timestamp: 1000000, Progress: &Progress{PositionMS: 100, DurationMS: 500, Speed: 1000}}
	if p, ok := m.Position(2000000); !ok || p != 500 {
		t.Fatal(p, ok)
	}
}

func TestFirstPresentationStateUsesCurrentClockEstimate(t *testing.T) {
	s := mediaSession(t)
	for _, at := range []int64{1, 2, 3} {
		s.filter.Update(0, 500, at)
	}
	if !s.filter.Ready() {
		t.Fatal("test clock did not converge")
	}
	// A receiver cannot prove the sender's first-state clock rule from an
	// uncertain estimate. It must still follow the role's scheduling rules.
	timestamp := s.now() + 1000000
	data, _ := json.Marshal(map[string]any{"metadata": Metadata{Timestamp: timestamp, Title: "first"}, "color": Colors{Timestamp: timestamp, Primary: []int{1, 2, 3}}})
	if err := s.serverState(data); err != nil {
		t.Fatal("first state was rejected based on local clock estimate", err)
	}
	if s.state.Metadata != nil || s.state.Colors != nil || s.pendingMetadata == nil || s.pendingColors == nil {
		t.Fatal("first future-mapped state was not held pending")
	}
	// A better clock mapping can make those same timestamps due without a
	// new server/state; no state may be lost while synchronization improves.
	// Synthetic samples 1/2/3 above may be ahead of a coarse Windows clock.
	// Keep this model update strictly monotonic without relying on test speed.
	s.filter.Update(timestamp+1000000, 1, max(int64(4), s.now()+1))
	// The improved mapping can land a few microseconds ahead of a quantized
	// time.Now value. Advance only this test epoch, not the real/system clock.
	s.epoch = s.epoch.Add(-2 * time.Millisecond)
	if err := s.scheduled(); err != nil {
		t.Fatal(err)
	}
	if s.state.Metadata == nil || s.state.Metadata.Title != "first" || s.state.Colors == nil {
		t.Fatal("first state did not follow the updated clock mapping")
	}
}
