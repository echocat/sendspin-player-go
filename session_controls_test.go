package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

type controlCountingStore struct {
	MemoryStore
	Saves atomic.Int32
}

func (s *controlCountingStore) Save(ctx context.Context, c Credentials) error {
	if err := s.MemoryStore.Save(ctx, c); err != nil {
		return err
	}
	s.Saves.Add(1)
	return nil
}

func controlSession(t *testing.T) (*session, *controlCountingStore) {
	t.Helper()
	store := &controlCountingStore{}
	client, err := New(context.Background(), Config{Name: "Control fields", Store: store, Roles: []Role{Player, Controller}, Output: unusedOutput{}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(client, context.Background(), func() {})
	s.channel = &wire.Channel{ServerID: client.ClientID(), Category: "lt"}
	s.hello, s.activated, s.admitted = true, true, true
	s.state.ActiveRoles = []Role{Player, Controller}
	s.state.Volume = 100
	return s, store
}

func TestUnofferedPlayerCommandsIgnoreArgumentBodies(t *testing.T) {
	for _, raw := range []string{
		`{"player":{"command":"future"}}`,
		`{"player":{"command":"future","volume":null,"mute":[],"output_delay_ms":{}}}`,
		`{"player":{"command":"VOLUME","volume":"unused","future":null}}`,
		`{"player":{"command":"","volume":null}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			s, store := controlSession(t)
			before := store.Saves.Load()
			// No keys/socket: an ignored command cannot send client/state.
			if err := s.serverCommand([]byte(raw)); err != nil {
				t.Fatal("ignored command validated argument body", err)
			}
			if store.Saves.Load() != before || s.state.Volume != 100 || s.state.Muted || s.state.OutputDelayMS != 0 {
				t.Fatal("ignored command changed persistent/player state")
			}
		})
	}
}

func TestInactiveOrMalformedPlayerCommandCannotUseIgnoreRule(t *testing.T) {
	for _, raw := range []string{`{"player":null}`, `{"player":[]}`, `{"player":{}}`, `{"player":{"command":null}}`, `{"player":{"command":4}}`} {
		s, _ := controlSession(t)
		if err := s.serverCommand([]byte(raw)); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("malformed discriminator ignored", raw, err)
		}
	}
	s, _ := controlSession(t)
	s.state.ActiveRoles = []Role{Controller}
	if err := s.serverCommand([]byte(`{"player":{"command":"future","volume":null}}`)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("inactive known role was ignored", err)
	}
}

func TestControllerRejectsNegativeOptionalSeekBound(t *testing.T) {
	for _, commands := range []string{`[]`, `["seek_relative"]`, `["seek"]`} {
		s, _ := controlSession(t)
		s.state.Controller = &ControllerState{Volume: 17, Repeat: "one"}
		old := s.state.Controller
		err := s.serverState([]byte(`{"controller":{"supported_commands":` + commands + `,"volume":100,"muted":false,"repeat":"off","shuffle":false,"seek_max_ms":-1}}`))
		if !errors.Is(err, protocol.ErrProtocol) || s.state.Controller != old {
			t.Fatal("negative seek maximum accepted or replaced valid state", commands, err)
		}
	}
}

func TestPlayerCommandArgumentMatrix(t *testing.T) {
	for _, tc := range []struct {
		raw, kind string
		value     any
	}{
		{`{"command":"volume","volume":0}`, "volume", 0},
		{`{"command":"volume","volume":100,"VOLUME":false,"future":null}`, "volume", 100},
		{`{"command":"mute","mute":false}`, "muted", false},
		{`{"command":"mute","mute":true}`, "muted", true},
		{`{"command":"set_output_delay","output_delay_ms":-9223372036854775808}`, "delay", 0},
		{`{"command":"set_output_delay","output_delay_ms":0}`, "delay", 0},
		{`{"command":"set_output_delay","output_delay_ms":17}`, "delay", 17},
		{`{"command":"set_output_delay","output_delay_ms":5000}`, "delay", 5000},
		{`{"command":"set_output_delay","output_delay_ms":9223372036854775807}`, "delay", 5000},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			kind, value, err := decodePlayerCommand([]byte(tc.raw))
			if err != nil || kind != tc.kind || value != tc.value {
				t.Fatalf("decoded %q/%v/%v, want %q/%v", kind, value, err, tc.kind, tc.value)
			}
		})
	}
	for _, raw := range []string{
		`{"command":"volume"}`, `{"command":"volume","volume":null}`, `{"command":"volume","volume":false}`, `{"command":"volume","volume":-1}`, `{"command":"volume","volume":101}`,
		`{"command":"volume","volume":1,"mute":false}`, `{"command":"volume","volume":1,"output_delay_ms":0}`,
		`{"command":"mute"}`, `{"command":"mute","mute":null}`, `{"command":"mute","mute":0}`, `{"command":"mute","mute":false,"volume":0}`, `{"command":"mute","mute":true,"output_delay_ms":0}`,
		`{"command":"set_output_delay"}`, `{"command":"set_output_delay","output_delay_ms":null}`, `{"command":"set_output_delay","output_delay_ms":"1"}`, `{"command":"set_output_delay","output_delay_ms":1.5}`,
		`{"command":"set_output_delay","output_delay_ms":0,"volume":0}`, `{"command":"set_output_delay","output_delay_ms":0,"mute":false}`,
	} {
		s, store := controlSession(t)
		before := store.Saves.Load()
		if err := s.serverCommand([]byte(`{"player":` + raw + `}`)); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("malformed command accepted", raw, err)
		}
		if store.Saves.Load() != before || s.state.Volume != 100 || s.state.Muted || s.state.OutputDelayMS != 0 {
			t.Fatal("invalid command mutated state")
		}
	}
}

func TestControllerStateFieldAndReplacementMatrix(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"supported_commands": []string{}, "volume": 100, "muted": false, "repeat": "off", "shuffle": false}
	}
	for _, field := range []string{"supported_commands", "volume", "muted", "repeat", "shuffle"} {
		for _, remove := range []bool{false, true} {
			state := base()
			if remove {
				delete(state, field)
			} else {
				state[field] = nil
			}
			raw, _ := json.Marshal(state)
			if _, err := decodeControllerState(raw); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("missing/null state field accepted", field, remove, err)
			}
		}
	}
	for _, extra := range []map[string]any{
		{"supported_commands": []any{nil}}, {"supported_commands": []any{4}}, {"supported_commands": []string{"future"}},
		{"volume": -1}, {"volume": 101}, {"muted": 0}, {"repeat": "future"}, {"shuffle": "false"},
		{"supported_commands": []string{"seek"}}, {"seek_max_ms": nil}, {"seek_max_ms": -1}, {"seek_max_ms": 1.5},
	} {
		state := base()
		for key, value := range extra {
			state[key] = value
		}
		raw, _ := json.Marshal(state)
		if _, err := decodeControllerState(raw); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("invalid controller state accepted", extra, err)
		}
	}
	s, _ := controlSession(t)
	if err := s.serverState([]byte(`{"controller":{"supported_commands":["seek","seek_relative"],"volume":0,"muted":true,"repeat":"one","shuffle":true,"seek_max_ms":9223372036854775807,"VOLUME":false,"future":null}}`)); err != nil {
		t.Fatal(err)
	}
	if s.state.Controller.SeekMaxMS == nil || *s.state.Controller.SeekMaxMS != math.MaxInt64 || s.state.Controller.Volume != 0 {
		t.Fatal("valid state/unknown exact-case fields mishandled")
	}
	old := *s.state.Controller
	if err := s.serverState([]byte(`{"future":null}`)); err != nil || !reflect.DeepEqual(old, *s.state.Controller) {
		t.Fatal("omitted controller object did not preserve state", err)
	}
	if err := s.serverState([]byte(`{"controller":{"supported_commands":["seek_relative","seek_relative"],"volume":100,"muted":false,"repeat":"off","shuffle":false}}`)); err != nil {
		t.Fatal(err)
	}
	if s.state.Controller.SeekMaxMS != nil || s.state.Controller.Muted || s.state.Controller.Shuffle || !slices.Equal(s.state.Controller.SupportedCommands, []string{"seek_relative", "seek_relative"}) {
		t.Fatal("state replacement merged fields or invented list uniqueness")
	}
}

func TestEveryControllerCommandHasExactArguments(t *testing.T) {
	want := []string{"play", "pause", "stop", "next", "previous", "volume", "mute", "repeat_off", "repeat_one", "repeat_all", "shuffle", "unshuffle", "switch", "seek", "seek_relative"}
	if !slices.Equal(controllerCommands, want) {
		t.Fatal("RC1 command inventory changed")
	}
	maximum := int64(100)
	zero := 0
	off := false
	start := int64(0)
	end := int64(100)
	negative := int64(-1)
	pastEnd := int64(101)
	for _, name := range controllerCommands {
		command := Command{Command: name}
		switch name {
		case "volume":
			command.Volume = &zero
		case "mute":
			command.Mute = &off
		case "seek":
			command.PositionMS = &start
		case "seek_relative":
			command.OffsetMS = &negative
		}
		if !validCommand(command, &maximum) {
			t.Fatal("valid command rejected", name)
		}
		if name != "volume" {
			wrong := command
			wrong.Volume = &zero
			if validCommand(wrong, &maximum) {
				t.Fatal("foreign argument accepted", name)
			}
		}
		if name != "mute" {
			wrong := command
			wrong.Mute = &off
			if validCommand(wrong, &maximum) {
				t.Fatal("foreign argument accepted", name)
			}
		}
		if name != "seek" {
			wrong := command
			wrong.PositionMS = &start
			if validCommand(wrong, &maximum) {
				t.Fatal("foreign argument accepted", name)
			}
		}
		if name != "seek_relative" {
			wrong := command
			wrong.OffsetMS = &negative
			if validCommand(wrong, &maximum) {
				t.Fatal("foreign argument accepted", name)
			}
		}
	}
	for _, position := range []*int64{nil, &negative, &pastEnd} {
		if validCommand(Command{Command: "seek", PositionMS: position}, &maximum) {
			t.Fatal("invalid seek accepted")
		}
	}
	if !validCommand(Command{Command: "seek", PositionMS: &end}, &maximum) || validCommand(Command{Command: "seek", PositionMS: &start}, nil) {
		t.Fatal("seek bound not enforced")
	}
	for _, offset := range []int64{math.MinInt64, 0, math.MaxInt64} {
		if !validCommand(Command{Command: "seek_relative", OffsetMS: &offset}, nil) {
			t.Fatal("signed relative seek was locally clamped")
		}
	}
}

func FuzzControlModels(f *testing.F) {
	for _, raw := range []string{`{"command":"future","volume":null}`, `{"command":"volume","volume":0}`, `{"command":"mute","mute":false}`, `{"command":"set_output_delay","output_delay_ms":9223372036854775807}`} {
		f.Add(uint8(0), []byte(raw))
	}
	f.Add(uint8(1), []byte(`{"supported_commands":[],"volume":100,"muted":false,"repeat":"off","shuffle":false}`))
	f.Add(uint8(1), []byte(`{"supported_commands":["seek"],"volume":0,"muted":true,"repeat":"all","shuffle":true,"seek_max_ms":0}`))
	f.Fuzz(func(t *testing.T, model uint8, data []byte) {
		if len(data) > 128<<10 {
			return
		}
		if model&1 == 0 {
			kind, value, err := decodePlayerCommand(data)
			if err != nil {
				return
			}
			var fields map[string]json.RawMessage
			if !utf8.Valid(data) || !protocol.Object(data) || json.Unmarshal(data, &fields) != nil {
				t.Fatal("invalid command JSON accepted")
			}
			var command *string
			if json.Unmarshal(fields["command"], &command) != nil || command == nil {
				t.Fatal("malformed command discriminator ignored")
			}
			expected := map[string]string{"volume": "volume", "mute": "muted", "set_output_delay": "delay"}[*command]
			if kind != expected {
				t.Fatalf("wrong command dispatch %q for %q", kind, *command)
			}
			if kind == "" {
				if value != nil {
					t.Fatal("ignored command has a value")
				}
				return
			}
			var argument string
			switch kind {
			case "volume":
				argument = "volume"
				v, ok := value.(int)
				if !ok || v < 0 || v > 100 {
					t.Fatal("volume outside declared range")
				}
			case "muted":
				argument = "mute"
				if _, ok := value.(bool); !ok {
					t.Fatal("mute is not boolean")
				}
			case "delay":
				argument = "output_delay_ms"
				v, ok := value.(int)
				if !ok || v < 0 || v > 5000 {
					t.Fatal("delay was not clamped")
				}
			}
			for _, key := range []string{"volume", "mute", "output_delay_ms"} {
				if raw, ok := fields[key]; ok {
					if key != argument || string(raw) == "null" {
						t.Fatal("foreign/null known argument accepted")
					}
				} else if key == argument {
					t.Fatal("required command argument absent")
				}
			}
			return
		}
		state, err := decodeControllerState(data)
		if err != nil {
			return
		}
		var fields map[string]json.RawMessage
		if !utf8.Valid(data) || !protocol.Object(data) || json.Unmarshal(data, &fields) != nil {
			t.Fatal("invalid controller JSON accepted")
		}
		for _, key := range []string{"supported_commands", "volume", "muted", "repeat", "shuffle"} {
			if _, ok := fields[key]; !ok {
				t.Fatal("required controller field absent", key)
			}
		}
		if state.SupportedCommands == nil || state.Volume < 0 || state.Volume > 100 || !slices.Contains([]string{"off", "one", "all"}, state.Repeat) {
			t.Fatal("invalid controller state")
		}
		for _, name := range state.SupportedCommands {
			if !slices.Contains(controllerCommands, name) {
				t.Fatal("unknown command offered")
			}
		}
		if state.SeekMaxMS != nil && *state.SeekMaxMS < 0 || slices.Contains(state.SupportedCommands, "seek") && state.SeekMaxMS == nil {
			t.Fatal("invalid seek bound accepted")
		}
		raw, _ := json.Marshal(state)
		again, err := decodeControllerState(raw)
		if err != nil || !reflect.DeepEqual(state, again) {
			t.Fatal("accepted controller state cannot roundtrip", err)
		}
	})
}

func TestControllerRequestsUseLatestCapabilityState(t *testing.T) {
	s, store := controlSession(t)
	s.state.Controller = &ControllerState{SupportedCommands: []string{}, Volume: 100, Repeat: "off"}
	before := store.Saves.Load()
	if err := s.request(request{ctx: context.Background(), kind: "command", value: Command{Command: "play"}}); !errors.Is(err, ErrInactiveRole) {
		t.Fatal("unoffered command sent", err)
	}
	s.state.Controller.SupportedCommands = []string{"seek"}
	maximum := int64(10)
	s.state.Controller.SeekMaxMS = &maximum
	invalid := int64(11)
	if err := s.request(request{ctx: context.Background(), kind: "command", value: Command{Command: "seek", PositionMS: &invalid}}); err == nil {
		t.Fatal("out-of-range seek sent")
	}
	s.clearRole("controller")
	if err := s.request(request{ctx: context.Background(), kind: "command", value: Command{Command: "seek"}}); !errors.Is(err, ErrInactiveRole) {
		t.Fatal("command sent without current state", err)
	}
	if store.Saves.Load() != before {
		t.Fatal("rejected controller request changed local settings")
	}
}

func TestControllerSnapshotCannotChangeCommandPermissions(t *testing.T) {
	s, _ := controlSession(t)
	s.client.active = s
	if err := s.serverState([]byte(`{"controller":{"supported_commands":["seek"],"volume":100,"muted":false,"repeat":"off","shuffle":false,"seek_max_ms":10}}`)); err != nil {
		t.Fatal(err)
	}
	state := s.client.Snapshot().Controller
	state.SupportedCommands[0] = "play"
	*state.SeekMaxMS = 999
	again := s.client.Snapshot().Controller
	if !slices.Equal(again.SupportedCommands, []string{"seek"}) || *again.SeekMaxMS != 10 {
		t.Fatal("caller changed actor permissions through a snapshot")
	}
}
