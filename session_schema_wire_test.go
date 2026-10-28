package sendspin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/wire"
	"github.com/flynn/noise"
)

var schemaRoles = []Role{Player, Controller, MetadataRole, ArtworkRole, VisualizerRole, ColorRole}

func schemaConfig() Config {
	return Config{
		Name: "Schema \"client\" 曲", Roles: slices.Clone(schemaRoles), Output: unusedOutput{},
		DeviceInfo:     &DeviceInfo{ProductName: "Model 曲", Manufacturer: "Maker", SoftwareVersion: "0.test"},
		Formats:        []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 24}, {Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16}},
		BufferCapacity: 1 << 20, VisualizerCapacity: 4321,
		RequiredLeadTime: 37 * time.Millisecond, MinBuffer: 23 * time.Millisecond,
		Artwork:    []ArtworkChannel{{Source: "album", Format: "png", Width: 20, Height: 30}, {Source: "none"}, {Source: "artist", Format: "jpeg", Width: 40, Height: 50}, {Source: "album", Format: "jpeg", Width: 60, Height: 70}},
		Visualizer: VisualizerConfig{Types: []string{"spectrum", "loudness", "peak"}, RateMax: 17, Spectrum: &Spectrum{Bins: 3, Scale: "mel", MinFrequency: 0, MaxFrequency: 16000}},
	}
}

// newSchemaSession uses the real encrypted SDK channel but directly drives the
// actor's dispatch methods, without timers or hardware. The scripted peer owns
// its framing and shares only the upstream Noise primitive (not independent crypto).
func newSchemaSession(t *testing.T, config Config, expectedSaves ...int32) (*session, *handshakePeer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	serverCredentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := Secret{92}
	credentials.Volume, credentials.Muted, credentials.OutputDelayMS = 73, true, 41
	credentials.Records = []PairingRecord{{ServerID: serverCredentials.ClientID(), PSK: key}}
	credentials.LastPlaybackServer = serverCredentials.ClientID()
	store := &controlCountingStore{}
	wantSaves := int32(1)
	if len(expectedSaves) == 1 {
		wantSaves = expectedSaves[0]
	}
	if err = store.Save(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	config.Store = store
	client, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append([]byte("sendspin-psk-id-v1"), key[:]...))
	peer := &handshakePeer{ctx: ctx, suite: client.cfg.CipherSuite, clientKey: mustPeerPublic(credentials.PrivateKey[:]), serverKey: serverCredentials.PrivateKey[:], serverID: serverCredentials.ClientID(), pskID: base64.RawURLEncoding.EncodeToString(hash[:]), psk: key[:]}
	ready, stop := make(chan error, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			ready <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		peer.conn = conn
		if err = peer.initialTransport(); err != nil {
			ready <- err
			return
		}
		ready <- nil
		select {
		case <-stop:
		case <-ctx.Done():
		}
	}))
	t.Cleanup(func() { cancel(); close(stop); server.Close() })
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	channel, err := wire.Handshake(ctx, conn, noise.DHKey{Private: credentials.PrivateKey[:], Public: mustPeerPublic(credentials.PrivateKey[:])}, client.cfg.CipherSuite, client.cfg.MaxMessageBytes, client.resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-ready; err != nil {
		t.Fatal(err)
	}
	s := newSession(client, ctx, cancel)
	s.channel = channel
	client.sessions[s] = true
	s.admitted = true // Admission is not the subject of these single-actor tests.
	client.active = s
	t.Cleanup(func() {
		persisted, err := store.Load(context.Background())
		if err != nil || store.Saves.Load() != wantSaves || !reflect.DeepEqual(persisted, credentials) {
			t.Error("schema/lifecycle test changed persistent settings", err)
		}
	})
	return s, peer
}

func expectSchemaPayload(t *testing.T, p *handshakePeer, kind string, want map[string]any) {
	t.Helper()
	gotKind, raw, err := p.readSecure()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil || gotKind != kind || !reflect.DeepEqual(got, want) {
		t.Fatalf("encrypted payload: kind=%s got=%s want=%v err=%v", gotKind, raw, want, err)
	}
}

func schemaHello(supported []Role, device, unpaired bool) map[string]any {
	roles := make([]any, 0, len(supported))
	for _, role := range supported {
		roles = append(roles, string(role))
	}
	want := map[string]any{"name": "Schema \"client\" 曲", "supported_roles": roles, "supported_pair_methods": map[string]any{"pairing_psk": map[string]any{}}, "unpaired_access": map[string]any{"enabled": unpaired}}
	if device {
		want["device_info"] = map[string]any{"product_name": "Model 曲", "manufacturer": "Maker", "software_version": "0.test"}
	}
	if slices.Contains(supported, Player) {
		want["player@v1_support"] = map[string]any{"buffer_capacity": float64(1048576), "supported_formats": []any{
			map[string]any{"codec": "pcm", "sample_rate": float64(8000), "channels": float64(1), "bit_depth": float64(24)},
			map[string]any{"codec": "pcm", "sample_rate": float64(48000), "channels": float64(2), "bit_depth": float64(16)},
		}}
	}
	if slices.Contains(supported, VisualizerRole) {
		want["visualizer@v1_support"] = map[string]any{"buffer_capacity": float64(4321)}
	}
	return want
}

// Expected JSON is authored independently of the SDK's state structs/encoder.
func schemaState(t *testing.T, roles []Role, available bool, artworkCount int) map[string]any {
	t.Helper()
	want := map[string]any{"available": available}
	if slices.Contains(roles, Player) {
		want["player"] = map[string]any{"volume": float64(73), "muted": true, "output_delay_ms": float64(41), "required_lead_time_ms": float64(37), "min_buffer_ms": float64(23), "supported_commands": []any{"volume", "mute", "set_output_delay"}}
	}
	if slices.Contains(roles, ArtworkRole) {
		var channels []any
		if err := json.Unmarshal([]byte(`[{"source":"album","format":"png","width":20,"height":30},{"source":"none"},{"source":"artist","format":"jpeg","width":40,"height":50},{"source":"album","format":"jpeg","width":60,"height":70}]`), &channels); err != nil {
			t.Fatal(err)
		}
		want["artwork"] = map[string]any{"channels": channels[:artworkCount]}
	}
	if slices.Contains(roles, VisualizerRole) {
		want["visualizer"] = map[string]any{"types": []any{"spectrum", "loudness", "peak"}, "rate_max": float64(17), "spectrum": map[string]any{"n_disp_bins": float64(3), "scale": "mel", "f_min": float64(0), "f_max": float64(16000)}}
	}
	return want
}

func schemaRoleSubset(mask int) []Role {
	roles := []Role{}
	for i, role := range schemaRoles {
		if mask&(1<<i) != 0 {
			roles = append(roles, role)
		}
	}
	return roles
}

func schemaReceive(t *testing.T, s *session, p *handshakePeer, kind string, payload any) {
	t.Helper()
	if err := p.sendSecure(peerJSON(kind, payload)); err != nil {
		t.Fatal(err)
	}
	frame, err := s.channel.Read(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatch(frame); err != nil {
		t.Fatal(kind, err)
	}
}

func TestHelloExactFieldsForEverySupportedRoleSubset(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for mask := range 64 {
			t.Run(fmt.Sprintf("%s/roles-%02x", suite, mask), func(t *testing.T) {
				cfg := schemaConfig()
				cfg.Roles, cfg.CipherSuite = schemaRoleSubset(mask), suite
				cfg.AllowUnpaired = mask%2 == 1
				device := mask%3 != 0
				if !device {
					cfg.DeviceInfo = nil
				}
				s, p := newSchemaSession(t, cfg)
				schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
				expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, device, cfg.AllowUnpaired))
			})
		}
	}
}

func TestStateExactFieldsForEveryActiveRoleSubset(t *testing.T) {
	for mask := range 64 {
		t.Run(fmt.Sprintf("roles-%02x", mask), func(t *testing.T) {
			s, p := newSchemaSession(t, schemaConfig())
			schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
			expectSchemaPayload(t, p, "client/hello", schemaHello(schemaRoles, true, false))
			roles := schemaRoleSubset(mask)
			schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": roles})
			if len(roles) == 0 {
				if err := s.sendState(); err != nil {
					t.Fatal(err)
				}
			}
			expectSchemaPayload(t, p, "client/state", schemaState(t, roles, !slices.Contains(roles, Player), 4))
			for _, at := range []int64{1, 2, 3} {
				s.filter.Update(0, 1, at)
			}
			if !s.filter.Ready() {
				t.Fatal("test clock did not converge")
			}
			if err := s.sendState(); err != nil {
				t.Fatal(err)
			}
			expectSchemaPayload(t, p, "client/state", schemaState(t, roles, true, 4))
			if err := s.request(request{ctx: s.ctx, kind: "available", value: false}); err != nil {
				t.Fatal(err)
			}
			expectSchemaPayload(t, p, "client/state", schemaState(t, roles, false, 4))
		})
	}
}

func TestHelloDeviceInfoOmitsEachUnconfiguredOptionalField(t *testing.T) {
	for mask := range 8 {
		cfg := schemaConfig()
		cfg.Roles = []Role{}
		cfg.DeviceInfo = &DeviceInfo{}
		info := map[string]any{}
		if mask&1 != 0 {
			cfg.DeviceInfo.ProductName, info["product_name"] = "Only product", "Only product"
		}
		if mask&2 != 0 {
			cfg.DeviceInfo.Manufacturer, info["manufacturer"] = "Only maker", "Only maker"
		}
		if mask&4 != 0 {
			cfg.DeviceInfo.SoftwareVersion, info["software_version"] = "Only version", "Only version"
		}
		s, p := newSchemaSession(t, cfg)
		schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
		want := schemaHello(cfg.Roles, false, false)
		want["device_info"] = info
		expectSchemaPayload(t, p, "client/hello", want)
	}
}

func TestStatePreferenceUpdateAndReactivationKeepFullState(t *testing.T) {
	s, p := newSchemaSession(t, schemaConfig())
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello(schemaRoles, true, false))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": schemaRoles})
	expectSchemaPayload(t, p, "client/state", schemaState(t, schemaRoles, false, 4))
	format := codec.Format{Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16}
	if err := s.request(request{ctx: s.ctx, kind: "format", value: format}); err != nil {
		t.Fatal(err)
	}
	want := schemaState(t, schemaRoles, false, 4)
	want["player"].(map[string]any)["format"] = map[string]any{"codec": "pcm", "sample_rate": float64(48000), "channels": float64(2), "bit_depth": float64(16)}
	expectSchemaPayload(t, p, "client/state", want)
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{ArtworkRole, VisualizerRole}})
	expectSchemaPayload(t, p, "client/state", schemaState(t, []Role{ArtworkRole, VisualizerRole}, true, 4))
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": schemaRoles})
	expectSchemaPayload(t, p, "client/state", want)
}

func TestStateReactivationReemitsOnlyCurrentRoleObjects(t *testing.T) {
	s, p := newSchemaSession(t, schemaConfig())
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
	expectSchemaPayload(t, p, "client/hello", schemaHello(schemaRoles, true, false))
	for _, roles := range [][]Role{schemaRoles, {Controller}, {MetadataRole, ColorRole}, schemaRoles, {ArtworkRole}, schemaRoles} {
		schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": roles})
		expectSchemaPayload(t, p, "client/state", schemaState(t, roles, !slices.Contains(roles, Player), 4))
	}
}

func TestArtworkStateEmitsOneToFourPositionalChannels(t *testing.T) {
	for count := 1; count <= 4; count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			cfg := schemaConfig()
			cfg.Roles, cfg.Artwork = []Role{ArtworkRole}, cfg.Artwork[:count]
			s, p := newSchemaSession(t, cfg)
			schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Peer"})
			expectSchemaPayload(t, p, "client/hello", schemaHello(cfg.Roles, true, false))
			schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": cfg.Roles})
			expectSchemaPayload(t, p, "client/state", schemaState(t, cfg.Roles, true, count))
			echo := cfg.Artwork
			if count == 2 {
				echo = echo[:1] // Server may omit the explicit trailing none position.
			}
			schemaReceive(t, s, p, "stream/start", map[string]any{"server_transmitted": 0, "artwork": map[string]any{"channels": echo}})
			if !s.streams["artwork"] || !reflect.DeepEqual(s.media.artworkChannels, echo) {
				t.Fatal("emitted positional declaration did not match effective server echo")
			}
		})
	}
}
