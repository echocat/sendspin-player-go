//go:build e2e

package sendspin

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
)

func referenceControlClient(t *testing.T) (*Client, referenceServer, *controlCountingStore, *outboundTestConnection) {
	t.Helper()
	ctx := context.Background()
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{121}
	ref := startReferenceWithOptions(t, cred, psk, false, map[string]any{"observe_controls": true})
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &controlCountingStore{}
	if err = store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{Name: "Control rules", Store: store, Roles: []Role{Player, Controller}, Output: clockOutput{}, Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}}, RequiredLeadTime: 35 * time.Millisecond, MinBuffer: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	connection := connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().Available })
	return client, ref, store, connection
}

func controlEvents(t *testing.T, ref referenceServer, kind string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, raw := range ref.control(t, map[string]any{"command": "status"})["events"].([]any) {
		event := raw.(map[string]any)
		if event["type"] == kind {
			events = append(events, event)
		}
	}
	return events
}

func TestE2EReferenceStaticTimingHintsSurviveReadinessAndAvailabilityChanges(t *testing.T) {
	client, ref, _, _ := referenceControlClient(t)
	// Observe actual decrypted client/state at the independently implemented
	// peer. Reference acceptance alone would not assert the emitted values.
	eventually(t, func() bool {
		var unready, ready bool
		for _, event := range controlEvents(t, ref, "player_state") {
			unready = unready || event["available"] == false
			ready = ready || event["available"] == true
		}
		return unready && ready
	})
	for _, available := range []bool{false, true} {
		before := len(controlEvents(t, ref, "player_state"))
		if err := client.SetAvailable(context.Background(), available); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool {
			events := controlEvents(t, ref, "player_state")
			return len(events) > before && events[len(events)-1]["available"] == available
		})
	}
	if err := client.SetOutputDelay(context.Background(), 137*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		events := controlEvents(t, ref, "player_state")
		return events[len(events)-1]["fields"].(map[string]any)["output_delay_ms"] == float64(137)
	})
	for _, event := range controlEvents(t, ref, "player_state") {
		fields := event["fields"].(map[string]any)
		if fields["required_lead_time_ms"] != float64(35) || fields["min_buffer_ms"] != float64(20) {
			t.Fatal("static hints changed on convergence/availability/delay", event)
		}
	}
}

func TestE2EReferencePlayerCommandsClampPersistAndEchoCompleteState(t *testing.T) {
	client, ref, store, _ := referenceControlClient(t)
	if err := client.SetAvailable(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return ref.control(t, map[string]any{"command": "player_status"})["available"] == false })
	for i, tc := range []struct {
		command       map[string]any
		volume, delay int
		muted         bool
	}{
		{map[string]any{"command": "mute", "mute": true}, 100, 0, true},
		{map[string]any{"command": "volume", "volume": 0, "future": nil}, 0, 0, true},
		{map[string]any{"command": "volume", "volume": 100}, 100, 0, true},
		{map[string]any{"command": "set_output_delay", "output_delay_ms": int64(math.MaxInt64)}, 100, 5000, true},
		{map[string]any{"command": "set_output_delay", "output_delay_ms": int64(math.MinInt64)}, 100, 0, true},
		{map[string]any{"command": "set_output_delay", "output_delay_ms": 17}, 100, 17, true},
		{map[string]any{"command": "mute", "mute": false}, 100, 17, false},
	} {
		before := len(controlEvents(t, ref, "player_state"))
		saves := store.Saves.Load()
		sendPairingFixtureMessage(t, ref, "server/command", map[string]any{"player": tc.command, "future_role": nil})
		eventually(t, func() bool {
			state := ref.control(t, map[string]any{"command": "player_status"})
			return state["volume"] == float64(tc.volume) && state["muted"] == tc.muted && state["output_delay_ms"] == float64(tc.delay) && len(controlEvents(t, ref, "player_state")) > before
		})
		pairingFixtureBarrier(t, client, ref, fmt.Sprint("player-command-", i))
		echoes := controlEvents(t, ref, "player_state")
		if len(echoes) != before+1 || store.Saves.Load() != saves+1 {
			t.Fatal("command was not persisted/echoed exactly once")
		}
		echo := echoes[len(echoes)-1]
		want := map[string]any{"volume": float64(tc.volume), "muted": tc.muted, "output_delay_ms": float64(tc.delay), "required_lead_time_ms": float64(35), "min_buffer_ms": float64(20), "supported_commands": []any{"volume", "mute", "set_output_delay"}}
		if echo["available"] != false || !reflect.DeepEqual(echo["fields"], want) {
			t.Fatal("echo was partial, unmuted, or included delay in timing hints", echo)
		}
		persisted, err := store.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Volume != tc.volume || persisted.Muted != tc.muted || persisted.OutputDelayMS != tc.delay {
			t.Fatal("persistent settings differ from actual echoed state")
		}
	}
	before := len(controlEvents(t, ref, "player_state"))
	saves := store.Saves.Load()
	old := client.Snapshot()
	for _, command := range []map[string]any{
		{"command": "future", "volume": nil, "mute": []any{}, "output_delay_ms": map[string]any{}},
		{"command": "VOLUME", "volume": "ignored"},
	} {
		sendPairingFixtureMessage(t, ref, "server/command", map[string]any{"player": command})
	}
	pairingFixtureBarrier(t, client, ref, "ignored-unoffered-commands")
	if len(controlEvents(t, ref, "player_state")) != before || store.Saves.Load() != saves {
		t.Fatal("unoffered command echoed or persisted")
	}
	if state := client.Snapshot(); state.Volume != old.Volume || state.Muted != old.Muted || state.OutputDelayMS != old.OutputDelayMS {
		t.Fatal("unoffered command changed settings")
	}
}

func expectedControllerFields(command Command) map[string]any {
	fields := map[string]any{"command": command.Command}
	if command.Volume != nil {
		fields["volume"] = strconv.Itoa(*command.Volume)
	}
	if command.Mute != nil {
		fields["mute"] = *command.Mute
	}
	if command.PositionMS != nil {
		fields["position_ms"] = strconv.FormatInt(*command.PositionMS, 10)
	}
	if command.OffsetMS != nil {
		fields["offset_ms"] = strconv.FormatInt(*command.OffsetMS, 10)
	}
	return fields
}

func TestE2EReferenceAllControllerCommandsUseLatestStateAndExactArguments(t *testing.T) {
	client, ref, _, _ := referenceControlClient(t)
	mediaCommand(t, client, ref, map[string]any{"command": "controller", "commands": controllerCommands, "seek_max_ms": int64(math.MaxInt64), "repeat": "all", "shuffle": true})
	state := client.Snapshot().Controller
	if state == nil || state.SeekMaxMS == nil || *state.SeekMaxMS != math.MaxInt64 || len(state.SupportedCommands) != len(controllerCommands) {
		t.Fatal("native controller capabilities were not received", state)
	}
	for _, name := range controllerCommands {
		if !slices.Contains(state.SupportedCommands, name) {
			t.Fatal("native capability missing", name)
		}
	}
	zero := 0
	off := false
	start := int64(0)
	offset := int64(-123)
	// Switch is sent last since native handling may change group membership.
	commands := append([]string(nil), controllerCommands...)
	commands = slices.DeleteFunc(commands, func(value string) bool { return value == "switch" })
	for _, name := range commands {
		command := Command{Command: name}
		switch name {
		case "volume":
			command.Volume = &zero
		case "mute":
			command.Mute = &off
		case "seek":
			command.PositionMS = &start
		case "seek_relative":
			command.OffsetMS = &offset
		}
		before := len(controlEvents(t, ref, "controller_command"))
		if err := client.Command(context.Background(), command); err != nil {
			t.Fatal(name, err)
		}
		eventually(t, func() bool { return len(controlEvents(t, ref, "controller_command")) == before+1 })
		events := controlEvents(t, ref, "controller_command")
		last := events[len(events)-1]
		if last["unexpected_keys"] != float64(0) || !reflect.DeepEqual(last["fields"], expectedControllerFields(command)) {
			t.Fatal("controller command changed on the wire", command, last)
		}
	}
	for _, tc := range []struct {
		name  string
		value int64
	}{{"seek", math.MaxInt64}, {"seek_relative", math.MinInt64}, {"seek_relative", 0}, {"seek_relative", math.MaxInt64}} {
		value := tc.value
		command := Command{Command: tc.name}
		if tc.name == "seek" {
			command.PositionMS = &value
		} else {
			command.OffsetMS = &value
		}
		before := len(controlEvents(t, ref, "controller_command"))
		if err := client.Command(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool { return len(controlEvents(t, ref, "controller_command")) == before+1 })
		events := controlEvents(t, ref, "controller_command")
		if !reflect.DeepEqual(events[len(events)-1]["fields"], expectedControllerFields(command)) {
			t.Fatal("seek argument lost precision, zero presence, or was locally clamped")
		}
	}
	mediaCommand(t, client, ref, map[string]any{"command": "controller", "commands": []string{"seek", "seek_relative"}, "seek_max_ms": 10})
	bad := int64(11)
	before := len(controlEvents(t, ref, "controller_command"))
	if err := client.Command(context.Background(), Command{Command: "seek", PositionMS: &bad}); err == nil {
		t.Fatal("obsolete seek bound was used")
	}
	if err := client.Command(context.Background(), Command{Command: "play"}); err == nil {
		t.Fatal("removed command remained offered")
	}
	mediaCommand(t, client, ref, map[string]any{"command": "controller", "commands": []string{"seek_relative"}})
	if client.Snapshot().Controller.SeekMaxMS != nil {
		t.Fatal("omitted seek maximum was merged from old state")
	}
	if err := client.Command(context.Background(), Command{Command: "seek", PositionMS: &start}); err == nil {
		t.Fatal("absolute seek allowed with unknown range")
	}
	pairingFixtureBarrier(t, client, ref, "rejected-controller-requests")
	if len(controlEvents(t, ref, "controller_command")) != before {
		t.Fatal("rejected commands reached the server")
	}
	mediaCommand(t, client, ref, map[string]any{"command": "roles", "roles": []string{"player@v1"}})
	if client.Snapshot().Controller != nil {
		t.Fatal("removed controller state survived")
	}
	if err := client.Command(context.Background(), Command{Command: "seek_relative", OffsetMS: &offset}); err == nil {
		t.Fatal("inactive controller accepted a command")
	}
	mediaCommand(t, client, ref, map[string]any{"command": "roles", "roles": []string{"player@v1", "controller@v1"}})
	if state := client.Snapshot().Controller; state == nil || state.SeekMaxMS != nil {
		t.Fatal("reactivation did not provide fresh complete controller state")
	}
	command := Command{Command: "switch"}
	if err := client.Command(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return len(controlEvents(t, ref, "controller_command")) == before+1 })
	events := controlEvents(t, ref, "controller_command")
	if !reflect.DeepEqual(events[len(events)-1]["fields"], expectedControllerFields(command)) {
		t.Fatal("switch command was not forwarded")
	}
}

func TestE2EReferenceMalformedPlayerCommandsCloseWithoutPersistenceOrEcho(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command map[string]any
	}{
		{"missing-discriminator", map[string]any{"volume": 0}},
		{"null-discriminator", map[string]any{"command": nil}},
		{"missing-volume", map[string]any{"command": "volume"}},
		{"null-volume", map[string]any{"command": "volume", "volume": nil}},
		{"range-volume", map[string]any{"command": "volume", "volume": 101}},
		{"foreign-argument", map[string]any{"command": "mute", "mute": false, "volume": 0}},
		{"missing-delay", map[string]any{"command": "set_output_delay"}},
		{"fractional-delay", map[string]any{"command": "set_output_delay", "output_delay_ms": 1.5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, ref, store, connection := referenceControlClient(t)
			if err := client.SetAvailable(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { return ref.control(t, map[string]any{"command": "player_status"})["available"] == false })
			before := len(controlEvents(t, ref, "player_state"))
			saves := store.Saves.Load()
			sendPairingFixtureMessage(t, ref, "server/command", map[string]any{"player": tc.command})
			requireSilentProtocolClose(t, connection, ref)
			if store.Saves.Load() != saves || len(controlEvents(t, ref, "player_state")) != before {
				t.Fatal("invalid command persisted or echoed state")
			}
		})
	}
}

func TestE2EReferenceMalformedControllerStatesCloseSilently(t *testing.T) {
	for _, tc := range []struct {
		name   string
		patch  map[string]any
		remove string
	}{
		{name: "missing-shuffle", remove: "shuffle"},
		{name: "null-supported", patch: map[string]any{"supported_commands": nil}},
		{name: "unknown-command", patch: map[string]any{"supported_commands": []string{"future"}}},
		{name: "seek-without-range", patch: map[string]any{"supported_commands": []string{"seek"}}},
		{name: "negative-unused-range", patch: map[string]any{"seek_max_ms": -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ref, store, connection := referenceControlClient(t)
			fields := map[string]any{"supported_commands": []string{}, "volume": 100, "muted": false, "repeat": "off", "shuffle": false}
			for key, value := range tc.patch {
				fields[key] = value
			}
			if tc.remove != "" {
				delete(fields, tc.remove)
			}
			saves := store.Saves.Load()
			sendPairingFixtureMessage(t, ref, "server/state", map[string]any{"controller": fields})
			requireSilentProtocolClose(t, connection, ref)
			if store.Saves.Load() != saves {
				t.Fatal("invalid controller state changed local settings")
			}
		})
	}
}
