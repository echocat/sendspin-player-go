package sendspin

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func schemaBinary(t *testing.T, s *session, p *handshakePeer, frame []byte) {
	t.Helper()
	packet, err := p.out.Encrypt(nil, nil, frame)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.conn.Write(p.ctx, websocket.MessageBinary, packet); err != nil {
		t.Fatal(err)
	}
	received, err := s.channel.Read(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatch(received); err != nil {
		t.Fatal(err)
	}
}

func TestPresentationStateAndTransfersSurviveSameVersionActivationAndRekeys(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		t.Run(suite, func(t *testing.T) {
			cfg := schemaConfig()
			cfg.Roles = slices.DeleteFunc(cfg.Roles, func(r Role) bool { return r == Player })
			cfg.CipherSuite = suite
			s, p := newSchemaSession(t, cfg)
			schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer", "languages": []string{"de-DE", "en"}})
			expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, false))
			schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": cfg.Roles})
			expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, true, 4))
			// Non-player roles may already be available with an unconverged clock.
			// Preserve a real initialized mapping, not just an empty filter pointer.
			s.filter.Update(17, 100, 1)
			mapped, uncertainty, ready := s.filter.ServerTime(123456), s.filter.Error(), s.filter.Ready()
			future := s.now() + int64(time.Hour/time.Microsecond)
			schemaReceive(t, s, p, "stream/start", map[string]any{"server_transmitted": 0, "artwork": map[string]any{"channels": cfg.Artwork}, "visualizer": map[string]any{"types": cfg.Visualizer.Types, "rate_max": 17, "spectrum": cfg.Visualizer.Spectrum}})
			schemaReceive(t, s, p, "server/state", map[string]any{
				"metadata":   map[string]any{"timestamp": 0, "title": "current", "progress": map[string]any{"track_progress": 73, "track_duration": 10000, "playback_speed": 0}},
				"color":      map[string]any{"timestamp": 0, "primary": []int{1, 2, 3}},
				"controller": map[string]any{"supported_commands": []string{"play"}, "volume": 31, "muted": false, "repeat": "off", "shuffle": false},
			})
			schemaReceive(t, s, p, "server/state", map[string]any{"metadata": map[string]any{"timestamp": future, "title": "pending"}, "color": map[string]any{"timestamp": future, "primary": []int{4, 5, 6}}})
			for _, channel := range []byte{0, 2} {
				schemaBinary(t, s, p, artworkAnnounce(channel, 0, 1))
				schemaBinary(t, s, p, []byte{8 + channel, 0, 7})
			}
			schemaBinary(t, s, p, artworkAnnounce(0, future, 3))
			schemaBinary(t, s, p, []byte{8, 0, 8, 9, 10})
			schemaBinary(t, s, p, artworkAnnounce(2, future, 3))
			schemaBinary(t, s, p, []byte{10, 0, 11}) // Still in flight across both rekeys.
			schemaBinary(t, s, p, visualizerFrame(16, future, 0x12, 0x34))
			schemaBinary(t, s, p, visualizerFrame(19, future, 0, 0, 0x80, 0, 0xff, 0xff))
			currentMetadata, pendingMetadata := s.state.Metadata, s.pendingMetadata
			currentColors, pendingColors, controller := s.state.Colors, s.pendingColors, s.state.Controller
			current0, current2, pending0, transfer := s.media.images[0], s.media.images[2], s.media.pendingImages[0], s.media.transfer
			visualLast := s.media.visualLast
			wantQueue := []visualFrame{{event: Visualization{Type: "loudness", Timestamp: future, Values: []uint16{0x1234}}, size: 11}, {event: Visualization{Type: "spectrum", Timestamp: future, Values: []uint16{0, 32768, 65535}}, size: 15}}
			if currentMetadata == nil || pendingMetadata == nil || currentColors == nil || pendingColors == nil || controller == nil || current0 == nil || current2 == nil || pending0 == nil || transfer == nil || !reflect.DeepEqual(s.media.visualQueue, wantQueue) {
				t.Fatal("test did not establish independent current/pending/transfer/queue state")
			}
			assertRetained := func() {
				t.Helper()
				if !slices.Equal(s.state.ActiveRoles, cfg.Roles) || len(s.active) != len(cfg.Roles) {
					t.Fatal("same-version or sticky active roles changed")
				}
				if s.state.Metadata != currentMetadata || s.pendingMetadata != pendingMetadata || s.state.Colors != currentColors || s.pendingColors != pendingColors || s.state.Controller != controller || s.media.images[0] != current0 || s.media.images[2] != current2 || s.media.pendingImages[0] != pending0 || s.media.transfer != transfer || transfer.received != 1 || !bytes.Equal(transfer.image.Data, []byte{11}) || !reflect.DeepEqual(s.media.visualQueue, wantQueue) || s.media.visualBytes != 26 || s.media.visualLast != visualLast || !s.streams["artwork"] || !s.streams["visualizer"] {
					t.Fatal("same-version activation/rekey lost or reinterpreted retained presentation state")
				}
				if s.filter.ServerTime(123456) != mapped || s.filter.Error() != uncertainty || s.filter.Ready() != ready || !slices.Equal(s.state.ServerLanguages, []string{"de-DE", "en"}) {
					t.Fatal("connection-wide clock/hello state was reset")
				}
			}
			// First exercise the actual same-version activation path, with both
			// explicit and sticky roles, independently of omission of server/state.
			for _, payload := range []map[string]any{{"activities": []string{}, "active_roles": cfg.Roles}, {"activities": []string{}}} {
				schemaReceive(t, s, p, "server/activate", payload)
				expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, true, 4))
				assertRetained()
			}
			for range 2 {
				oldHash := bytes.Clone(p.hash)
				hs, raw, err := p.noise1(p.hash, handshakeFault{})
				if err != nil {
					t.Fatal(err)
				}
				_, payload, err := peerMessage(raw)
				if err != nil {
					t.Fatal(err)
				}
				schemaReceive(t, s, p, "noise/handshake", json.RawMessage(payload))
				if !s.awaitingActivation {
					t.Fatal("rekey did not establish an activation barrier")
				}
				assertRetained()
				if err = p.noise2(hs, true); err != nil || bytes.Equal(oldHash, p.hash) {
					t.Fatal("actual encrypted rekey response/key change missing", err)
				}
				// New-key activation omits roles; full state must be the next SDK
				// application response, not another hello or a cleared state.
				schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}})
				expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, true, 4))
				assertRetained()
			}
			// Finish the old in-flight transfer using the second new transport key.
			schemaBinary(t, s, p, []byte{10, 0, 12, 13})
			if s.media.transfer != nil || s.media.pendingImages[2] == nil || !bytes.Equal(s.media.pendingImages[2].Data, []byte{11, 12, 13}) || s.media.images[2] != current2 {
				t.Fatal("transfer did not resume exactly once under new keys")
			}
			// Deterministically advance this directly driven test's clock epoch;
			// no sleeps, hardware clock, physical display or load claim is involved.
			for len(s.client.events) > 0 {
				<-s.client.events
			}
			s.epoch = s.epoch.Add(-2 * time.Hour)
			if err := s.scheduled(); err != nil {
				t.Fatal(err)
			}
			if s.state.Metadata != pendingMetadata || s.state.Colors != pendingColors || s.pendingMetadata != nil || s.pendingColors != nil || !bytes.Equal(s.media.images[0].Data, []byte{8, 9, 10}) || !bytes.Equal(s.media.images[2].Data, []byte{11, 12, 13}) || s.media.pendingImages[0] != nil || s.media.pendingImages[2] != nil || s.media.visualBytes != 0 || len(s.media.visualQueue) != 0 {
				t.Fatal("retained scheduled states did not apply after rekeys")
			}
			var events []Visualization
			for len(s.client.events) > 0 {
				event := <-s.client.events
				if event.Visualization != nil {
					events = append(events, *event.Visualization)
				}
			}
			if !reflect.DeepEqual(events, []Visualization{wantQueue[0].event, wantQueue[1].event}) {
				t.Fatal("queued decoded values were lost, reordered or emitted twice", events)
			}
		})
	}
}
