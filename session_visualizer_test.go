package sendspin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func visualizerSession(t *testing.T) *session {
	t.Helper()
	client, err := New(context.Background(), Config{Name: "Visualizer fields", Store: &MemoryStore{}, Roles: []Role{VisualizerRole}, Visualizer: VisualizerConfig{Types: []string{"loudness", "beat", "f_peak", "spectrum", "peak"}, RateMax: 30, Spectrum: &Spectrum{Bins: 4, Scale: "lin", MinFrequency: 0, MaxFrequency: 20000}}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, context.Background(), func() {})
	s.channel = &wire.Channel{ServerID: client.ClientID(), Category: "lt"}
	s.hello, s.activated, s.admitted = true, true, true
	s.state.ActiveRoles = client.cfg.Roles
	s.state.Available = true
	return s
}

func TestVisualizerConditionalFieldsAndSpectrumPresence(t *testing.T) {
	for _, raw := range []string{
		`{"types":["loudness"],"rate_max":30,"tracks_downbeats":false}`,
		`{"types":[],"rate_max":0,"spectrum":{"n_disp_bins":4,"scale":"lin","f_min":0,"f_max":20000}}`,
		`{"types":["spectrum"],"rate_max":30,"spectrum":{"n_disp_bins":4,"scale":"lin","f_max":20000}}`,
	} {
		s := visualizerSession(t)
		if err := s.startStream([]byte(`{"server_transmitted":0,"visualizer":` + raw + `}`)); !errors.Is(err, protocol.ErrProtocol) || s.streams["visualizer"] {
			t.Fatal("invalid visualizer declaration accepted", raw, err)
		}
	}
}

func TestVisualizerEchoDoesNotInventTypeUniqueness(t *testing.T) {
	s := visualizerSession(t)
	if err := s.startStream([]byte(`{"server_transmitted":0,"visualizer":{"types":["loudness","loudness"],"rate_max":30}}`)); err != nil {
		t.Fatal("subset echo rejected solely for a duplicate", err)
	}
	if !slices.Equal(s.media.visualizer.Types, []string{"loudness"}) {
		t.Fatal("redundant echo entries were retained on the per-frame lookup path")
	}
	types := make([]string, 10000)
	for i := range types {
		types[i] = "peak"
	}
	raw, _ := json.Marshal(map[string]any{"types": types, "rate_max": 30})
	config, _, err := decodeVisualizerStream(raw, s.client.cfg.Visualizer)
	if err != nil || !slices.Equal(config.Types, []string{"peak"}) || cap(config.Types) > len(s.client.cfg.Visualizer.Types) {
		t.Fatal("redundant peer input expanded retained per-frame lookup state", err)
	}
}

func TestVisualizerUnusedSpectrumStillMustBeValidWhenProvided(t *testing.T) {
	store := &MemoryStore{}
	_, err := New(context.Background(), Config{Name: "Optional spectrum", Store: store, Roles: []Role{VisualizerRole}, Visualizer: VisualizerConfig{Types: []string{"loudness"}, RateMax: 30, Spectrum: &Spectrum{Bins: 0, Scale: "future", MinFrequency: 0, MaxFrequency: 0}}})
	if err == nil {
		t.Fatal("invalid optional spectrum accepted and eligible for client/state emission")
	}
	if _, err = store.Load(context.Background()); !errors.Is(err, ErrNoCredentials) {
		t.Fatal("invalid config created credentials", err)
	}
}

func visualizerFrame(id byte, timestamp int64, payload ...byte) []byte {
	frame := binary.BigEndian.AppendUint64([]byte{id}, uint64(timestamp))
	return append(frame, payload...)
}

func startVisualizerTestStream(t *testing.T, s *session) {
	t.Helper()
	if err := s.startStream([]byte(`{"server_transmitted":0,"visualizer":{"types":["loudness","beat","f_peak","spectrum","peak"],"rate_max":30,"tracks_downbeats":true,"spectrum":{"n_disp_bins":4,"scale":"lin","f_min":0,"f_max":20000}}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestVisualizerStreamSchemaMatrix(t *testing.T) {
	s := visualizerSession(t)
	for _, raw := range []string{
		`{}`, `{"types":null,"rate_max":30}`, `{"types":[null],"rate_max":30}`, `{"types":["future"],"rate_max":30}`,
		`{"types":[],"rate_max":null}`, `{"types":[],"rate_max":-1}`, `{"types":[],"rate_max":31}`,
		`{"types":["beat"],"rate_max":30}`, `{"types":["beat"],"rate_max":30,"tracks_downbeats":null}`,
		`{"types":["spectrum"],"rate_max":30}`, `{"types":["spectrum"],"rate_max":30,"spectrum":null}`,
		`{"types":["spectrum"],"rate_max":30,"spectrum":{"n_disp_bins":3,"scale":"lin","f_min":0,"f_max":20000}}`,
		`{"types":["spectrum"],"rate_max":30,"spectrum":{"n_disp_bins":4,"scale":"log","f_min":0,"f_max":20000}}`,
	} {
		if _, _, err := decodeVisualizerStream([]byte(raw), s.client.cfg.Visualizer); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("invalid visualizer config accepted", raw, err)
		}
	}
	for _, raw := range []string{`{"types":[],"rate_max":0}`, `{"types":["beat"],"rate_max":30,"tracks_downbeats":false}`, `{"types":["loudness"],"rate_max":10,"TYPES":null,"future":false}`} {
		if _, _, err := decodeVisualizerStream([]byte(raw), s.client.cfg.Visualizer); err != nil {
			t.Fatal("valid reduced echo rejected", raw, err)
		}
	}
}

func TestVisualizerWireLengthsAndValues(t *testing.T) {
	for _, tc := range []struct {
		id       byte
		payload  []byte
		values   []uint16
		strength byte
	}{
		{16, []byte{0x80, 1}, []uint16{32769}, 0},
		{17, []byte{0xfe}, nil, 0},
		{18, []byte{3, 232, 0xff, 0xff}, []uint16{1000, 65535}, 0},
		{19, []byte{0, 0, 0, 1, 0x80, 0, 0xff, 0xff}, []uint16{0, 1, 32768, 65535}, 0},
		{20, []byte{255}, nil, 255},
	} {
		s := visualizerSession(t)
		startVisualizerTestStream(t, s)
		frame := visualizerFrame(tc.id, s.now()+5_000_000, tc.payload...)
		if err := s.visualization(frame); err != nil {
			t.Fatal(err)
		}
		if len(s.media.visualQueue) != 1 || !slices.Equal(s.media.visualQueue[0].event.Values, tc.values) || s.media.visualQueue[0].event.Strength != tc.strength {
			t.Fatal("binary value/endianness changed", tc.id)
		}
		for length := 0; length < len(frame)+3; length++ {
			if length == len(frame) {
				continue
			}
			bad := append([]byte(nil), frame...)
			if length < len(bad) {
				bad = bad[:length]
			} else {
				bad = append(bad, make([]byte, length-len(bad))...)
			}
			fresh := visualizerSession(t)
			startVisualizerTestStream(t, fresh)
			if err := fresh.visualization(bad); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("wrong payload length accepted", tc.id, length, err)
			}
		}
	}
	s := visualizerSession(t)
	startVisualizerTestStream(t, s)
	if err := s.visualization(visualizerFrame(18, s.now()+5_000_000, 0, 0, 0, 1)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("zero frequency/nonzero amplitude accepted", err)
	}
}

func TestVisualizerBeatReservedBitsAreIgnored(t *testing.T) {
	for _, tracks := range []bool{false, true} {
		s := visualizerSession(t)
		startVisualizerTestStream(t, s)
		s.media.downbeats = tracks
		stamp := s.now() + 5_000_000
		for flags := 0; flags <= 255; flags++ {
			err := s.visualization(visualizerFrame(17, stamp, byte(flags)))
			if flags&1 != 0 && !tracks {
				if !errors.Is(err, protocol.ErrProtocol) {
					t.Fatal("unadvertised downbeat accepted")
				}
				continue
			}
			if err != nil {
				t.Fatal("reserved flags changed beat validation", flags, tracks, err)
			}
			last := s.media.visualQueue[len(s.media.visualQueue)-1].event
			if last.Downbeat != (flags&1 != 0) || last.Strength != 0 || last.Values != nil {
				t.Fatal("reserved bits affected public event", flags, last)
			}
		}
	}
}

func TestVisualizerOrderingDiscardCapacityAndStreamBoundaries(t *testing.T) {
	s := visualizerSession(t)
	startVisualizerTestStream(t, s)
	stamp := s.now() + 5_000_000
	if err := s.visualization(visualizerFrame(16, stamp, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.visualization(visualizerFrame(20, stamp, 2)); err != nil {
		t.Fatal("equal cross-type timestamp rejected", err)
	}
	old := append([]visualFrame(nil), s.media.visualQueue...)
	if err := s.startStream([]byte(`{"server_transmitted":0,"visualizer":{"types":["loudness","peak"],"rate_max":10}}`)); err != nil || !reflect.DeepEqual(s.media.visualQueue, old) {
		t.Fatal("in-place update reset buffered data", err)
	}
	if err := s.visualization(visualizerFrame(20, stamp-1, 2)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("configuration update reset timestamp order")
	}
	if err := s.stopStream("stream/clear", []byte(`{"server_transmitted":0,"roles":["visualizer"]}`)); err != nil {
		t.Fatal(err)
	}
	if s.media.visualBytes != 0 || len(s.media.visualQueue) != 0 || s.media.visualLast != nil || !s.streams["visualizer"] {
		t.Fatal("clear did not reset queue/order without ending stream")
	}
	if err := s.visualization(visualizerFrame(20, stamp-2, 2)); err != nil {
		t.Fatal("clear did not permit lower timestamps", err)
	}
	if err := s.stopStream("stream/end", []byte(`{"roles":["visualizer"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.visualization(visualizerFrame(20, stamp, 2)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("frame accepted outside stream")
	}
	startVisualizerTestStream(t, s)
	s.state.Available = false
	if err := s.visualization(visualizerFrame(16, stamp-3, 0, 0)); err != nil || s.media.visualBytes != 0 {
		t.Fatal("unavailable valid frame was buffered/closed", err)
	}
	if err := s.visualization(visualizerFrame(20, stamp-4, 2)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("discard bypassed timestamp validation")
	}
	s = visualizerSession(t)
	startVisualizerTestStream(t, s)
	if err := s.visualization(visualizerFrame(16, 0, 0, 0)); err != nil || len(s.media.visualQueue) != 0 {
		t.Fatal("past frame buffered instead of dropped", err)
	}
	s = visualizerSession(t)
	startVisualizerTestStream(t, s)
	s.client.cfg.VisualizerCapacity = 22
	for i := 0; i < 2; i++ {
		if err := s.visualization(visualizerFrame(16, stamp, 0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.visualization(visualizerFrame(16, stamp, 0, 0)); !errors.Is(err, protocol.ErrProtocol) || s.media.visualBytes != 22 {
		t.Fatal("local buffer limit not enforced", err)
	}
}

func TestVisualizerEventsOwnValuesAndUnavailableDrainReleasesBytes(t *testing.T) {
	s := visualizerSession(t)
	startVisualizerTestStream(t, s)
	stamp := s.now() + 5_000_000
	first := visualizerFrame(16, stamp, 0, 1)
	if err := s.visualization(first); err != nil {
		t.Fatal(err)
	}
	if err := s.visualization(visualizerFrame(16, stamp, 0, 2)); err != nil {
		t.Fatal(err)
	}
	first[9] = 255
	if s.media.visualQueue[0].event.Values[0] != 1 {
		t.Fatal("queued values alias decrypted frame")
	}
	s.filter.Update(stamp+1_000_000, 1, s.now()+1)
	if err := s.scheduled(); err != nil {
		t.Fatal(err)
	}
	var values []*Visualization
	for len(s.client.events) > 0 {
		event := <-s.client.events
		if event.Visualization != nil {
			values = append(values, event.Visualization)
		}
	}
	if len(values) != 2 || values[0].Values[0] != 1 || values[1].Values[0] != 2 || s.media.visualBytes != 0 {
		t.Fatal("due frame delivery or byte accounting changed")
	}
	values[0].Values[0] = 99
	if values[1].Values[0] != 2 {
		t.Fatal("successive events share writable data")
	}
	s = visualizerSession(t)
	startVisualizerTestStream(t, s)
	stamp = s.now() + 5_000_000
	if err := s.visualization(visualizerFrame(20, stamp, 255)); err != nil {
		t.Fatal(err)
	}
	s.state.Available = false
	s.filter.Update(stamp+1_000_000, 1, s.now()+1)
	if err := s.scheduled(); err != nil {
		t.Fatal(err)
	}
	if s.media.visualBytes != 0 || len(s.media.visualQueue) != 0 {
		t.Fatal("unavailable due frames retained memory")
	}
	for len(s.client.events) > 0 {
		event := <-s.client.events
		if event.Visualization != nil {
			t.Fatal("unavailable frame emitted")
		}
	}
}

func FuzzVisualizerStream(f *testing.F) {
	for _, raw := range []string{
		`{"types":[],"rate_max":0}`,
		`{"types":["beat"],"rate_max":30,"tracks_downbeats":false}`,
		`{"types":["spectrum"],"rate_max":30,"spectrum":{"n_disp_bins":4,"scale":"lin","f_min":0,"f_max":20000}}`,
		`{"types":["loudness","loudness"],"rate_max":30}`,
	} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128<<10 {
			return
		}
		requested := VisualizerConfig{Types: []string{"loudness", "beat", "f_peak", "spectrum", "peak"}, RateMax: 30, Spectrum: &Spectrum{Bins: 4, Scale: "lin", MinFrequency: 0, MaxFrequency: 20000}}
		config, downbeats, err := decodeVisualizerStream(data, requested)
		if err != nil {
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || config.Types == nil || config.RateMax < 0 || config.RateMax > 30 {
			t.Fatal("invalid stream config accepted")
		}
		for _, name := range []string{"types", "rate_max"} {
			if _, ok := fields[name]; !ok {
				t.Fatal("required stream field missing")
			}
		}
		for _, typ := range config.Types {
			if !slices.Contains(requested.Types, typ) {
				t.Fatal("unrequested type accepted")
			}
		}
		_, hasTracks := fields["tracks_downbeats"]
		if hasTracks != slices.Contains(config.Types, "beat") || downbeats && !hasTracks {
			t.Fatal("downbeat field conditionality lost")
		}
		_, hasSpectrum := fields["spectrum"]
		if hasSpectrum != slices.Contains(config.Types, "spectrum") {
			t.Fatal("spectrum field conditionality lost")
		}
		if hasSpectrum {
			if config.Spectrum == nil || *config.Spectrum != *requested.Spectrum {
				t.Fatal("spectrum request changed")
			}
			var spectrum map[string]json.RawMessage
			_ = json.Unmarshal(fields["spectrum"], &spectrum)
			for _, name := range []string{"n_disp_bins", "scale", "f_min", "f_max"} {
				if _, ok := spectrum[name]; !ok {
					t.Fatal("required spectrum field missing")
				}
			}
		}
	})
}
