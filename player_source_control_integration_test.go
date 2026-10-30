package sendspin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

// These tests drive the combined session actor over the existing encrypted
// schema fixture. No render/input hardware or timer-driven actor is involved;
// channel handshakes and worker completion establish every lifecycle boundary.
func newPlayerSourceControlSession(t *testing.T, input *fakeSourceInput, target PlayerControlTarget, suite ...string) (*session, *handshakePeer, PlayerControlBinding) {
	t.Helper()
	cfg := Config{
		Name: "Combined source/player control", Roles: []Role{Player, SourceRole},
		Output: unusedOutput{}, Input: input, SourceFormat: audio.Format{SampleRate: 8000, Channels: 1},
		Formats:       []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}},
		PlayerControl: &PlayerControlConfig{},
	}
	if len(suite) != 0 {
		cfg.CipherSuite = suite[0]
	}
	s, p := newSchemaSession(t, cfg)
	t.Cleanup(func() { s.client.endPlayerControlSession(s); s.closeSource() })
	binding, err := s.client.BindPlayerControl(target)
	if err != nil {
		t.Fatal(err)
	}
	schemaReceive(t, s, p, "server/hello", map[string]any{
		"name": "Combined peer", "source@v1_support": map[string]any{"supported_codecs": []string{"pcm", "flac"}},
	})
	if kind, _, err := p.readSecure(); err != nil || kind != "client/hello" {
		t.Fatal("combined hello", kind, err)
	}
	sourceReady(s)
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{Player, SourceRole}})
	readPlayerSourceControlState(t, p, true, true, true, target.Initial)
	// Establish a genuine player stream as well as the separately authorized
	// capture stream, without starting the hardware-independent render worker.
	schemaReceive(t, s, p, "stream/start", map[string]any{
		"server_transmitted": 1, "player": map[string]any{"codec": "pcm", "sample_rate": 8000, "channels": 1, "bit_depth": 16},
	})
	if input.opens.Load() != 0 {
		t.Fatal("hello/activation/player stream opened capture")
	}
	return s, p, binding
}

func readPlayerSourceControlState(t *testing.T, p *handshakePeer, available, player, source bool, report PlayerControlReport) {
	t.Helper()
	playerState := map[string]any{
		"output_delay_ms": float64(41), "required_lead_time_ms": float64(0), "min_buffer_ms": float64(0),
	}
	commands := []any{}
	if report.Volume != nil {
		playerState["volume"] = float64(*report.Volume)
		if report.Capabilities.Volume {
			commands = append(commands, "volume")
		}
	}
	if report.Muted != nil {
		playerState["muted"] = *report.Muted
		if report.Capabilities.Mute {
			commands = append(commands, "mute")
		}
	}
	playerState["supported_commands"] = append(commands, "set_output_delay")
	want := map[string]any{"available": available}
	if player {
		want["player"] = playerState
	}
	if source {
		want["source"] = map[string]any{}
	}
	expectSchemaPayload(t, p, "client/state", want)
}

func receivePlayerSourceControl(t *testing.T, s *session, p *handshakePeer, payload string) error {
	t.Helper()
	if err := p.sendSecure(peerJSON("server/command", json.RawMessage(payload))); err != nil {
		t.Fatal(err)
	}
	frame, err := s.channel.Read(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s.dispatch(frame)
}

func awaitPlayerSourceControl(t *testing.T, s *session, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-s.ctx.Done():
		t.Fatal(message, s.ctx.Err())
	}
}

func playerSourceWritableTarget(set func(context.Context, int) error) PlayerControlTarget {
	return PlayerControlTarget{
		Initial: PlayerControlReport{
			Capabilities: PlayerControlCapabilities{Volume: true, Mute: true},
			Volume:       controlPointer(20), Muted: controlPointer(true),
		},
		SetVolume: set, SetMuted: func(context.Context, bool) error { return nil },
	}
}

func assertPlayerSourceCapture(t *testing.T, s *session, input *fakeSourceInput, capture *sourceCapture, opens int32) {
	t.Helper()
	if s.source.capture != capture || !s.source.open || s.source.pending || !s.streams["source"] || !s.streams["player"] || !s.state.Available || input.opens.Load() != opens || input.closes.Load() != opens-1 {
		t.Fatalf("combined stream/capture changed: source=%+v streams=%v available=%v opens=%d closes=%d", s.source, s.streams, s.state.Available, input.opens.Load(), input.closes.Load())
	}
}

func TestPlayerSourceControlIgnoredPlayerBodyStillStopsSource(t *testing.T) {
	for _, raw := range []string{
		`{"player":{"command":"future","volume":null,"mute":[],"output_delay_ms":{}},"source":{"command":"stop"}}`,
		`{"source":{"command":"stop"},"player":{"command":"volume","volume":null,"mute":[]}}`,
		`{"player":{"command":"mute","mute":42},"source":{"command":"stop"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			input := newFakeSourceInput()
			target := PlayerControlTarget{Initial: PlayerControlReport{Volume: controlPointer(0), Muted: controlPointer(true)}}
			s, p, _ := newPlayerSourceControlSession(t, input, target)
			sourceStart(t, s, p, input)
			capture := s.source.capture
			assertPlayerSourceCapture(t, s, input, capture, 1)
			if err := receivePlayerSourceControl(t, s, p, raw); err != nil {
				t.Fatal("unoffered player body blocked valid source stop", err)
			}
			expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
			awaitPlayerSourceControl(t, s, capture.done, "source stop did not close capture")
			if input.closes.Load() != 1 || s.source.open || s.source.pending || s.streams["source"] || !s.streams["player"] {
				t.Fatal("source stop lost or player stream stopped")
			}
			// A following state is a wire barrier: no duplicate end, capture
			// restart or software mutation may have been hidden behind the stop.
			if err := s.sendState(); err != nil {
				t.Fatal(err)
			}
			readPlayerSourceControlState(t, p, true, true, true, target.Initial)
			if input.opens.Load() != 1 || s.client.Snapshot().PlayerControl.VolumeRequest != nil {
				t.Fatal("ignored command caused a side effect")
			}
		})
	}
}

func TestPlayerSourceControlOfferedMalformedPlayerCannotAuthorizeCapture(t *testing.T) {
	for _, raw := range []string{
		`{"source":{"command":"start"},"player":{"command":"volume","volume":null}}`,
		`{"player":{"command":"volume","volume":101},"source":{"command":"start"}}`,
		`{"source":{"command":"start"},"player":{"command":"mute","mute":0}}`,
		`{"player":{"command":"set_output_delay","output_delay_ms":null},"source":{"command":"start"}}`,
		`{"source":{"command":"start"},"player":{"command":false}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			input := newFakeSourceInput()
			var writes atomic.Int32
			target := playerSourceWritableTarget(func(context.Context, int) error { writes.Add(1); return nil })
			s, p, _ := newPlayerSourceControlSession(t, input, target)
			if err := receivePlayerSourceControl(t, s, p, raw); !errors.Is(err, ErrProtocol) {
				t.Fatal("malformed offered player command accepted", err)
			}
			if s.source.capture != nil || s.source.pending || s.source.open || input.opens.Load() != 0 || writes.Load() != 0 {
				t.Fatal("malformed combined command launched an asynchronous side effect")
			}
			if state := s.client.Snapshot().PlayerControl; state.VolumeRequest != nil || state.MuteRequest != nil {
				t.Fatal("malformed combined command queued a player write")
			}
		})
	}
}

func TestPlayerSourceControlMalformedSourceCannotQueuePlayerWrite(t *testing.T) {
	for _, raw := range []string{
		`{"player":{"command":"volume","volume":35},"source":null}`,
		`{"source":{"command":"future"},"player":{"command":"volume","volume":35}}`,
		`{"player":{"command":"volume","volume":35},"source":{"command":false}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			input := newFakeSourceInput()
			var writes atomic.Int32
			s, p, _ := newPlayerSourceControlSession(t, input, playerSourceWritableTarget(func(context.Context, int) error { writes.Add(1); return nil }))
			if err := receivePlayerSourceControl(t, s, p, raw); !errors.Is(err, ErrProtocol) {
				t.Fatal("malformed source object accepted", err)
			}
			if state := s.client.Snapshot().PlayerControl; state.VolumeRequest != nil || writes.Load() != 0 || s.source.capture != nil || input.opens.Load() != 0 {
				t.Fatal("malformed source caused partial combined-command side effects")
			}
		})
	}
}

func TestPlayerSourceControlLiveReadbackAndCapabilityChangesPreserveBothStreams(t *testing.T) {
	input := newFakeSourceInput()
	var writes atomic.Int32
	target := playerSourceWritableTarget(func(context.Context, int) error { writes.Add(1); return nil })
	s, p, binding := newPlayerSourceControlSession(t, input, target)
	sourceStart(t, s, p, input)
	capture := s.source.capture
	for _, report := range []PlayerControlReport{
		{Volume: controlPointer(35), Muted: controlPointer(false)}, // Live read-only.
		{Err: errors.New("fake host unavailable")},                 // Controls/knowledge lost, not audio availability.
		{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(1)},
		{Capabilities: PlayerControlCapabilities{Mute: true}, Muted: controlPointer(true)},
		{Capabilities: PlayerControlCapabilities{Volume: true, Mute: true}, Volume: controlPointer(100), Muted: controlPointer(false)},
	} {
		var err error
		binding, err = s.client.ReportPlayerControl(binding, report)
		if err != nil {
			t.Fatal(err)
		}
		// Dispatch exactly the notification branch used by session.run, without
		// introducing network/timing goroutines into this deterministic fixture.
		select {
		case <-s.controlUpdates:
		default:
			t.Fatal("live report did not notify admitted session")
		}
		if err := s.sendState(); err != nil {
			t.Fatal(err)
		}
		readPlayerSourceControlState(t, p, true, true, true, report)
		assertPlayerSourceCapture(t, s, input, capture, 1)
		if state := s.client.Snapshot(); state.VolumeKnown != (report.Volume != nil) || state.MutedKnown != (report.Muted != nil) {
			t.Fatal("combined state invented optional observations")
		}
	}
	// Verify that capture can still send audio, not merely that its flags survived.
	first := time.Now()
	capture.push(sourceChunk{first: first, pcm: make([]byte, 320)})
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	if frame := sourceAudio(t, p); len(frame) != 329 {
		t.Fatal("source data path lost during readback changes")
	}
	if writes.Load() != 0 {
		t.Fatal("capability/readback update wrote hardware")
	}
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
}

func TestPlayerSourceControlRekeyCancelsWritesWithoutRestartingAuthorizedSource(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		t.Run(suite, func(t *testing.T) {
			input := newFakeSourceInput()
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var calls atomic.Int32
			target := playerSourceWritableTarget(func(ctx context.Context, _ int) error {
				if calls.Add(1) == 1 {
					close(started)
					<-ctx.Done()
					close(canceled)
					<-release
				}
				return nil // Intentionally stale success after cancellation.
			})
			s, p, _ := newPlayerSourceControlSession(t, input, target, suite)
			sourceStart(t, s, p, input)
			capture := s.source.capture
			if err := s.testControlRequest("volume", 50); err != nil {
				t.Fatal(err)
			}
			awaitPlayerSourceControl(t, s, started, "external write was not started")
			if err := s.testControlRequest("volume", 60); err != nil {
				t.Fatal(err)
			}
			hs, raw, err := p.noise1(p.hash, handshakeFault{})
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
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("rekey did not cancel external write")
			}
			if got := s.client.Snapshot().PlayerControl.VolumeRequest.Status; got != "canceled" {
				t.Fatal("queued generation retained after rekey", got)
			}
			assertPlayerSourceCapture(t, s, input, capture, 1)
			capture.push(sourceChunk{first: monotime.Now().Add(-time.Second), pcm: make([]byte, 320)})
			capture.mu.Lock()
			capture.queue[0].queuedAt = capture.queue[0].queuedAt.Add(-time.Second)
			capture.mu.Unlock()
			if err := s.advanceSource(); err != nil || sourceQueueLen(capture) != 1 {
				t.Fatal("source wrote before post-rekey activation", err)
			}
			if err := s.request(request{ctx: s.ctx, kind: "volume", value: 70}); err == nil {
				t.Fatal("local player request bypassed rekey barrier")
			}
			schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}})
			readPlayerSourceControlState(t, p, true, true, true, target.Initial)
			if err := s.advanceSource(); err != nil || sourceQueueLen(capture) != 0 {
				t.Fatal("source did not discard stale pre-activation backlog", err)
			}
			assertPlayerSourceCapture(t, s, input, capture, 1)
			// Stop before releasing the stale setter so its final nil cannot
			// revive requests, change observations or consume source authorization.
			if err := s.stopSource(); err != nil {
				t.Fatal(err)
			}
			expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
			awaitPlayerSourceControl(t, s, capture.done, "explicit source stop did not finish")
			if err := s.advanceSource(); err != nil {
				t.Fatal(err)
			}
			if err := s.sendState(); err != nil {
				t.Fatal(err)
			}
			readPlayerSourceControlState(t, p, true, true, true, target.Initial)
			if input.opens.Load() != 1 || s.source.pending || s.source.open {
				t.Fatal("post-rekey state automatically authorized capture")
			}
			if calls.Load() != 1 {
				t.Fatal("queued old-generation external write executed")
			}
			close(release)
			waitControl(t, func() bool {
				control := s.client.playerControl
				control.mu.Lock()
				defer control.mu.Unlock()
				return !control.running
			})
			state := s.client.Snapshot()
			if state.PlayerControl.VolumeRequest.Status != "canceled" || state.Volume != 20 || !state.Muted || calls.Load() != 1 || input.opens.Load() != 1 || s.source.pending || s.source.open {
				t.Fatal("stale successful setter revived control/source state", state)
			}
		})
	}
}

func TestPlayerSourceControlRoleLossSeparatesPlayerGenerationAndSourceAuthorization(t *testing.T) {
	input := newFakeSourceInput()
	started, canceled := make(chan struct{}), make(chan struct{})
	target := playerSourceWritableTarget(func(ctx context.Context, _ int) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})
	s, p, _ := newPlayerSourceControlSession(t, input, target)
	sourceStart(t, s, p, input)
	capture := s.source.capture
	if err := s.testControlRequest("volume", 45); err != nil {
		t.Fatal(err)
	}
	awaitPlayerSourceControl(t, s, started, "external write was not started")
	// Player stream must end before removing its role. Source remains active.
	schemaReceive(t, s, p, "stream/end", map[string]any{"roles": []string{"player"}})
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{SourceRole}})
	readPlayerSourceControlState(t, p, true, false, true, target.Initial)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("player role loss did not cancel external generation")
	}
	if s.source.capture != capture || !s.source.open || !s.streams["source"] || input.opens.Load() != 1 || input.closes.Load() != 0 {
		t.Fatal("player role loss restarted/stopped source")
	}
	if err := s.testControlRequest("volume", 50); !errors.Is(err, ErrInactiveRole) {
		t.Fatal("player request accepted after role loss", err)
	}
	// Removing the source role, unlike the player role, must consume capture
	// authorization and emit stream/end before the next state declaration.
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{Player}})
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
	readPlayerSourceControlState(t, p, true, true, false, target.Initial)
	awaitPlayerSourceControl(t, s, capture.done, "source role loss did not finish capture")
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{Player, SourceRole}})
	readPlayerSourceControlState(t, p, true, true, true, target.Initial)
	if input.opens.Load() != 1 || input.closes.Load() != 1 || s.source.open || s.source.pending || s.source.capture != nil {
		t.Fatal("reactivation resurrected prior source authorization")
	}
	sourceStart(t, s, p, input)
	if input.opens.Load() != 2 || !s.source.open {
		t.Fatal("new explicit source authorization did not work after role loss")
	}
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
}

func TestPlayerSourceControlAvailabilityOrderingAndSourceClockGate(t *testing.T) {
	input := newFakeSourceInput()
	target := PlayerControlTarget{Initial: PlayerControlReport{Volume: controlPointer(0), Muted: controlPointer(true)}}
	s, p, _ := newPlayerSourceControlSession(t, input, target)
	sourceStart(t, s, p, input)
	capture := s.source.capture
	if err := s.request(request{ctx: s.ctx, kind: "available", value: false}); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
	readPlayerSourceControlState(t, p, false, true, true, target.Initial)
	awaitPlayerSourceControl(t, s, capture.done, "unavailability did not finish capture")
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	if s.source.pending || s.source.capture != nil {
		t.Fatal("unavailable start retained capture authorization")
	}
	// Check the new Source readiness gate without Player masking a regression.
	schemaReceive(t, s, p, "stream/end", map[string]any{"roles": []string{"player"}})
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{SourceRole}})
	readPlayerSourceControlState(t, p, false, false, true, target.Initial)
	s.filter = newSession(s.client, s.ctx, func() {}).filter
	if err := s.request(request{ctx: s.ctx, kind: "available", value: true}); err != nil {
		t.Fatal(err)
	}
	readPlayerSourceControlState(t, p, false, false, true, target.Initial)
	schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	if s.source.pending || s.source.capture != nil {
		t.Fatal("unconverged source-only client authorized capture")
	}
	sourceReady(s)
	if err := s.sendState(); err != nil {
		t.Fatal(err)
	}
	readPlayerSourceControlState(t, p, true, false, true, target.Initial)
	if input.opens.Load() != 1 || s.source.pending || s.source.open {
		t.Fatal("readiness restoration implicitly restarted capture")
	}
	sourceStart(t, s, p, input)
	if input.opens.Load() != 2 || !reflect.DeepEqual(s.state.ActiveRoles, []Role{SourceRole}) || !slices.Equal(s.client.Snapshot().ActiveStreams, []string{"source"}) {
		t.Fatal("explicit source-only authorization/state failed")
	}
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
}
