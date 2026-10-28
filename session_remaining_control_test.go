package sendspin

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/playback"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func schemaRekeyCategory(t *testing.T, s *session, p *handshakePeer, category string) {
	t.Helper()
	key := s.client.credentials.Records[0].PSK
	if category == "pr" {
		key = s.client.credentials.PairingPSK
	}
	if category == "sn" {
		key = sentinelPSK
	}
	p.psk = bytes.Clone(key[:])
	h := sha256.Sum256(append([]byte("sendspin-psk-id-v1"), key[:]...))
	p.pskID = base64.RawURLEncoding.EncodeToString(h[:])
	hs, raw, err := p.noise1(p.hash, handshakeFault{body: fmt.Sprintf(`{"psk_id":"$PSK","psk_category":%q}`, category)})
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := peerMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	schemaReceive(t, s, p, "noise/handshake", json.RawMessage(payload))
	if err = p.noise2(hs, true); err != nil {
		t.Fatal(err)
	}
	if s.channel.Category != category || !s.awaitingActivation {
		t.Fatal("new trust category not installed", category)
	}
}

func TestRekeyOmittedRolesAreEvaluatedUnderNewTrustCategory(t *testing.T) {
	for _, category := range []string{"lt", "pr", "sn"} {
		for _, allow := range []bool{false, true} {
			for _, explicit := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/allow-%t/explicit-%t", category, allow, explicit), func(t *testing.T) {
					cfg := schemaConfig()
					cfg.Roles = []Role{Controller, MetadataRole, ColorRole}
					cfg.AllowUnpaired = allow
					s, p := newSchemaSession(t, cfg)
					schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
					expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, allow))
					schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": cfg.Roles})
					expectSchemaPayload(t, p, "client/state", map[string]any{"available": true})
					schemaReceive(t, s, p, "server/state", map[string]any{"metadata": map[string]any{"timestamp": 0, "title": "old"}, "color": map[string]any{"timestamp": 0, "primary": []int{1, 2, 3}}, "controller": map[string]any{"supported_commands": []string{}, "volume": 11, "muted": false, "repeat": "off", "shuffle": false}})
					s.pendingMetadata = &Metadata{Timestamp: s.now() + 1_000_000, Title: "pending"}
					s.pendingColors = &Colors{Timestamp: s.now() + 1_000_000, Primary: []int{4, 5, 6}}
					schemaRekeyCategory(t, s, p, category)
					payload := map[string]any{"activities": []string{}}
					if explicit {
						payload["active_roles"] = []Role{}
					}
					schemaReceive(t, s, p, "server/activate", payload)
					kept := !explicit && (category == "lt" || allow)
					if kept {
						expectSchemaPayload(t, p, "client/state", map[string]any{"available": true})
						if !slices.Equal(s.state.ActiveRoles, cfg.Roles) || s.state.Metadata == nil || s.pendingMetadata == nil || s.state.Colors == nil || s.pendingColors == nil || s.state.Controller == nil {
							t.Fatal("authorized sticky roles/state were cleared")
						}
					} else {
						if len(s.state.ActiveRoles) != 0 || len(s.active) != 0 || s.state.Metadata != nil || s.pendingMetadata != nil || s.state.Colors != nil || s.pendingColors != nil || s.state.Controller != nil {
							t.Fatal("obsolete trust retained sticky roles/state")
						}
					}
					if s.awaitingActivation {
						t.Fatal("subsequent activation kept barrier")
					}
				})
			}
		}
	}
}

func TestAddingPairingPreservesAllAuthorizedRolesStatesAndGroup(t *testing.T) {
	cfg := schemaConfig()
	cfg.AllowUnpaired = true
	s, p := newSchemaSession(t, cfg)
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, true))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": cfg.Roles})
	expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, false, 4))
	for _, at := range []int64{1, 2, 3} {
		s.filter.Update(0, 1, at)
	}
	if err := s.sendState(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, true, 4))
	schemaReceive(t, s, p, "group/update", map[string]any{"group_id": "unchanged-group", "group_name": "Existing group", "playback_state": "playing"})
	schemaReceive(t, s, p, "server/state", map[string]any{"metadata": map[string]any{"timestamp": 0, "title": "current"}, "color": map[string]any{"timestamp": 0, "primary": []int{1, 2, 3}}, "controller": map[string]any{"supported_commands": []string{"play"}, "volume": 31, "muted": false, "repeat": "off", "shuffle": false}})
	schemaReceive(t, s, p, "stream/start", map[string]any{"server_transmitted": 0, "player": cfg.Formats[0], "artwork": map[string]any{"channels": cfg.Artwork}, "visualizer": map[string]any{"types": []string{"loudness"}, "rate_max": 9}})
	s.pendingMetadata = &Metadata{Timestamp: s.now() + 1_000_000, Title: "future"}
	s.pendingColors = &Colors{Timestamp: s.now() + 1_000_000, Primary: []int{4, 5, 6}}
	s.media.pendingImages[0] = &Artwork{Channel: 0, Timestamp: s.now() + 1_000_000, Data: []byte{8}}
	s.media.visualQueue = []visualFrame{{event: Visualization{Type: "loudness", Timestamp: s.now() + 1_000_000, Values: []uint16{99}}, size: 11}}
	s.media.visualBytes = 11
	md, colors, controller, pmd, pc, pa := s.state.Metadata, s.state.Colors, s.state.Controller, s.pendingMetadata, s.pendingColors, s.media.pendingImages[0]
	group := s.state.Group
	queue := slices.Clone(s.media.visualQueue)
	snapshot := s.client.Snapshot()
	schemaRekeyCategory(t, s, p, "pr")
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{"pairing"}, "pairing": map[string]any{"method": "pairing_psk"}})
	expectSchemaPayload(t, p, "client/pair-init", map[string]any{"pairing_index": float64(1)})
	kind, raw, err := p.readSecure()
	if err != nil || kind != "client/pair-finalize" {
		t.Fatal("pair-finalize missing", err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || fields["long_term_psk"] == nil {
		t.Fatal("invalid PSK disclosure shape")
	}
	expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, true, 4))
	if !reflect.DeepEqual(s.client.Snapshot(), snapshot) || !slices.Equal(s.state.ActiveRoles, cfg.Roles) || s.state.Group != group || s.state.Metadata != md || s.state.Colors != colors || s.state.Controller != controller || s.pendingMetadata != pmd || s.pendingColors != pc || s.media.pendingImages[0] != pa || !reflect.DeepEqual(s.media.visualQueue, queue) || !s.streams["player"] || !s.streams["artwork"] || !s.streams["visualizer"] {
		t.Fatal("pairing alone changed group/authorized role state")
	}
	// A non-pairing replacement cancels the disclosed key, without a server ack.
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}})
	expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, true, 4))
	if s.pendingPSK != nil || s.pairingAttempt {
		t.Fatal("replacement retained attempt")
	}
}

func TestRekeyActivationUsesNewCategoryForActivitiesAndExplicitRoles(t *testing.T) {
	for _, tc := range []struct {
		name, category, method, want string
		allow, explicit              bool
		activities                   []string
	}{
		{"long-term-no-pairing", "lt", "pairing_psk", "unauthorized", false, false, []string{"pairing"}},
		{"pairing-playback-denied", "pr", "", "pairing_required", false, false, []string{"playback"}},
		{"sentinel-playback-denied", "sn", "", "pairing_required", false, false, []string{"playback"}},
		{"pairing-explicit-role-denied", "pr", "", "pairing_required", false, true, []string{}},
		{"sentinel-explicit-role-denied", "sn", "", "pairing_required", false, true, []string{}},
		{"pairing-wrong-method", "pr", "static_pairing_code", "method_not_supported", false, false, []string{"pairing"}},
		{"sentinel-wrong-category", "sn", "pairing_psk", "method_not_supported", false, false, []string{"pairing"}},
		{"sentinel-unoffered-method", "sn", "static_pairing_code", "method_not_supported", true, false, []string{"pairing"}},
		{"sentinel-unauthorized-precedence", "sn", "pairing_psk", "unauthorized", false, false, []string{"pairing", "playback"}},
		{"pairing-authorized-no-playback", "pr", "pairing_psk", "pairing", false, false, []string{"pairing"}},
		{"long-term-playback", "lt", "", "state", false, false, []string{"playback"}},
		{"pairing-unpaired-playback", "pr", "", "state", true, false, []string{"playback"}},
		{"sentinel-unpaired-playback", "sn", "", "state", true, false, []string{"playback"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := schemaConfig()
			cfg.Roles = []Role{Controller}
			cfg.AllowUnpaired = tc.allow
			expected := int32(1)
			if tc.want == "state" {
				expected = 2
			}
			s, p := newSchemaSession(t, cfg, expected)
			schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
			expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, tc.allow))
			schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": cfg.Roles})
			expectSchemaPayload(t, p, "client/state", map[string]any{"available": true})
			schemaRekeyCategory(t, s, p, tc.category)
			payload := map[string]any{"activities": tc.activities}
			if tc.explicit {
				payload["active_roles"] = cfg.Roles
			}
			if tc.method != "" {
				payload["pairing"] = map[string]any{"method": tc.method}
			}
			if err := p.sendSecure(peerJSON("server/activate", payload)); err != nil {
				t.Fatal(err)
			}
			frame, err := s.channel.Read(s.ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = s.dispatch(frame)
			if tc.want == "unauthorized" || tc.want == "pairing_required" {
				var goodbye *GoodbyeError
				if !errors.As(err, &goodbye) || goodbye.Reason != tc.want {
					t.Fatal("wrong trust refusal", err)
				}
				expectSchemaPayload(t, p, "client/goodbye", map[string]any{"reason": tc.want})
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "method_not_supported" {
				expectSchemaPayload(t, p, "pair/abort", map[string]any{"reason": "method_not_supported"})
				if s.pendingPSK != nil || s.pairingAttempt {
					t.Fatal("unsupported category/method disclosed a PSK")
				}
				schemaReceive(t, s, p, "group/update", map[string]any{"group_id": "still-open", "group_name": "open", "playback_state": "stopped"})
				if s.state.Group.ID != "still-open" {
					t.Fatal("refusal left unusable channel")
				}
				return
			}
			if tc.want == "pairing" {
				expectSchemaPayload(t, p, "client/pair-init", map[string]any{"pairing_index": float64(1)})
				if kind, _, err := p.readSecure(); err != nil || kind != "client/pair-finalize" {
					t.Fatal("new-category PSK pairing did not start", err)
				}
				if len(s.state.ActiveRoles) != 0 {
					t.Fatal("non-playback-capable pairing retained old roles")
				}
				return
			}
			expectSchemaPayload(t, p, "client/state", map[string]any{"available": true})
			if !slices.Equal(s.state.ActiveRoles, cfg.Roles) || !s.activated || s.awaitingActivation {
				t.Fatal("authorized new-category playback lost sticky roles")
			}
		})
	}
}

func TestFutureExtensionsDoNotGrantCommandOrFeaturePermissions(t *testing.T) {
	s, p := newSchemaSession(t, Config{Name: "Schema \"client\" 曲", Roles: []Role{Controller}, DeviceInfo: schemaConfig().DeviceInfo})
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello([]Role{Controller}, true, false))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{Controller}})
	expectSchemaPayload(t, p, "client/state", map[string]any{"available": true})
	schemaReceive(t, s, p, "server/state", map[string]any{"controller": map[string]any{"supported_commands": []string{"play"}, "volume": 0, "muted": false, "repeat": "off", "shuffle": false}})
	schemaReceive(t, s, p, "future/feature", map[string]any{"supported_commands": []string{"future"}, "_offered": true})
	schemaReceive(t, s, p, "server/state", map[string]any{"_future_support": true})
	if err := s.request(request{ctx: s.ctx, kind: "command", value: Command{Command: "future"}}); !errors.Is(err, ErrInactiveRole) {
		t.Fatal("silence/unknown extension granted feature", err)
	}
	if err := s.request(request{ctx: s.ctx, kind: "command", value: Command{Command: "play"}}); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client/command", map[string]any{"controller": map[string]any{"command": "play"}})
}

func TestCoreMessageDispatchDoesNotRequireAnApplicationRole(t *testing.T) {
	s, p := newSchemaSession(t, Config{Name: "Schema \"client\" 曲", Roles: []Role{}})
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello([]Role{}, false, false))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{}})
	if err := s.request(request{ctx: s.ctx, kind: "leave"}); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client/leave", map[string]any{})
	s.timeOutstanding, s.arrival = 100, 900
	if err := s.send("client/time", map[string]int64{"client_transmitted": 100}); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client/time", map[string]any{"client_transmitted": float64(100)})
	schemaReceive(t, s, p, "server/time", map[string]any{"client_transmitted": 100, "server_received": 500, "server_transmitted": 600})
	if s.timeOutstanding != 0 {
		t.Fatal("roleless clock reply not processed")
	}
	schemaReceive(t, s, p, "group/update", map[string]any{"group_id": "roleless", "group_name": "Roleless group", "playback_state": "stopped"})
	schemaReceive(t, s, p, "server/state", map[string]any{})
	schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{}})
	schemaRekeyCategory(t, s, p, "pr")
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{"pairing"}, "active_roles": []Role{}, "pairing": map[string]any{"method": "pairing_psk"}})
	expectSchemaPayload(t, p, "client/pair-init", map[string]any{"pairing_index": float64(1)})
	if kind, _, err := p.readSecure(); err != nil || kind != "client/pair-finalize" {
		t.Fatal("roleless PSK flow failed", err)
	}
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}})
	schemaReceive(t, s, p, "server/unpair", map[string]any{})
	for _, kind := range []string{"server/hello", "server/init", "server/error", "client/init", "client/hello", "client/time", "client/state", "client/command", "client/leave", "client/goodbye", "client/pair-init", "client/pair-finalize", "client/pair-pending", "client/pair-auth", "client/pair-confirm", "client/pair-retry", "server/pair-init", "server/pair-auth", "server/pair-confirm", "server/pair-finalize", "stream/clear", "stream/end"} {
		if err := s.dispatch(coreFrame(t, kind, map[string]any{})); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("roleless core direction/sequence check bypassed", kind, err)
		}
	}
	if s.state.Group.ID != "roleless" || len(s.state.ActiveRoles) != 0 {
		t.Fatal("roleless core changed role/group state")
	}
}

func TestGoodbyeReasonVocabularyHasExactPayloads(t *testing.T) {
	s, p := newSchemaSession(t, Config{Name: "Schema \"client\" 曲", Roles: []Role{}})
	for _, reason := range []string{"another_server", "shutdown", "restart", "user_request", "unauthorized", "pairing_required", "concurrent_attempt", "unpaired"} {
		s.goodbye(reason)
		expectSchemaPayload(t, p, "client/goodbye", map[string]any{"reason": reason})
	}
}

func TestUndefinedGoodbyeReasonsCannotReachTheWire(t *testing.T) {
	s, p := newSchemaSession(t, Config{Name: "Schema \"client\" 曲", Roles: []Role{}})
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello([]Role{}, false, false))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{}})
	for _, reason := range []string{"", "future", "SHUTDOWN", "user-request"} {
		if validGoodbyeReason(reason) {
			t.Fatal("undefined reason accepted")
		}
		s.goodbye(reason)
		if err := s.failGoodbye(reason); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("invalid reason did not fail closed", err)
		}
	}
	if err := s.request(request{ctx: s.ctx, kind: "leave"}); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client/leave", map[string]any{})
}

func TestMultiRoleInPlaceStartKeepsOldAudioAndPresentationBuffers(t *testing.T) {
	s := proofSession(t)
	cfg := schemaConfig()
	s.client.cfg.Formats = []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 24}, {Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16}}
	anchor := time.Now().Add(-time.Second)
	out := &proofOutput{next: anchor, writes: make(chan proofWrite, 8)}
	s.engine = playback.New(out, 1<<20, func(stamp int64) (time.Time, error) {
		return anchor.Add(time.Duration(stamp-1_000_000) * time.Microsecond), nil
	})
	start := func(player codec.Format, visual map[string]any) {
		b, _ := json.Marshal(map[string]any{"server_transmitted": 0, "player": player, "artwork": map[string]any{"channels": cfg.Artwork}, "visualizer": visual})
		if err := s.startStream(b); err != nil {
			t.Fatal(err)
		}
	}
	start(s.client.cfg.Formats[0], map[string]any{"types": cfg.Visualizer.Types, "rate_max": 17, "spectrum": cfg.Visualizer.Spectrum})
	if err := s.binary(proofAudio(1_000_000, 0, bytes.Repeat([]byte{0, 0, 32}, 160))); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork(artworkAnnounce(0, s.now()+1_000_000, 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.artwork([]byte{8, 0, 9}); err != nil {
		t.Fatal(err)
	}
	transfer := s.media.transfer
	stamp := s.now() + 1_000_000
	if err := s.visualization(visualizerFrame(19, stamp, 0, 1, 0, 2, 0, 3)); err != nil {
		t.Fatal(err)
	}
	queue := slices.Clone(s.media.visualQueue)
	cursor := s.media.visualLast
	start(s.client.cfg.Formats[1], map[string]any{"types": []string{"loudness"}, "rate_max": 9})
	if s.media.transfer != transfer || transfer.received != 1 || !reflect.DeepEqual(s.media.visualQueue, queue) || s.media.visualLast != cursor || len(s.streams) != 3 {
		t.Fatal("in-place start ended/redecoded another role")
	}
	if err := s.binary(proofAudio(1_020_000, 0, bytes.Repeat([]byte{0, 32, 0, 192}, 960))); err != nil {
		t.Fatal(err)
	}
	w := runProofEngine(t, s, out, 2)
	if w[0].format != (audio.Format{SampleRate: 8000, Channels: 1}) || w[1].format != (audio.Format{SampleRate: 48000, Channels: 2}) || len(w[0].samples) != 160 || len(w[1].samples) != 1920 {
		t.Fatal("format update discarded/reinterpreted old queued audio")
	}
	for _, v := range w[0].samples {
		if v != .25 {
			t.Fatal("old PCM24 changed", v)
		}
	}
	for i, v := range w[1].samples {
		want := float32(.25)
		if i%2 == 1 {
			want = -.5
		}
		if v != want {
			t.Fatal("new interleaved PCM16 changed", i, v)
		}
	}
}

func TestRoleRemovalAfterEndClearsAllRemainingRoleState(t *testing.T) {
	s := proofSession(t)
	s.streams = map[string]bool{"player": true, "artwork": true, "visualizer": true}
	s.decoder = &unusedDecoder{}
	s.state.Metadata = &Metadata{Title: "old"}
	s.pendingMetadata = &Metadata{Title: "pending"}
	s.state.Colors = &Colors{Primary: []int{1, 2, 3}}
	s.pendingColors = &Colors{Primary: []int{4, 5, 6}}
	s.state.Controller = &ControllerState{}
	s.media.images[0] = &Artwork{Channel: 0, Data: []byte{1}}
	s.media.pendingImages[0] = &Artwork{Channel: 0, Data: []byte{2}}
	s.media.visualQueue = []visualFrame{{size: 11}}
	s.media.visualBytes = 11
	if err := s.stopStream("stream/end", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.activate([]byte(`{"activities":[],"active_roles":[]}`)); err != nil {
		t.Fatal(err)
	}
	if len(s.streams) != 0 || s.decoder != nil || s.media.transfer != nil || s.media.images[0] != nil || s.media.pendingImages[0] != nil || s.media.visualBytes != 0 || len(s.media.visualQueue) != 0 || s.state.Metadata != nil || s.pendingMetadata != nil || s.state.Colors != nil || s.pendingColors != nil || s.state.Controller != nil || len(s.state.ActiveRoles) != 0 {
		t.Fatal("end/removal retained role output/state")
	}
}
