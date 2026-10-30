package sendspin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

func controlPointer[T any](v T) *T { return &v }

func externalSession(t *testing.T, cfg PlayerControlConfig) (*Client, *session, *controlCountingStore) {
	t.Helper()
	store := &controlCountingStore{}
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	cred.Volume, cred.Muted, cred.OutputDelayMS = 1, true, 41
	if err = store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	c, err := New(context.Background(), Config{Name: "External", Store: store, Output: unusedOutput{}, Roles: []Role{Player, Controller}, PlayerControl: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := newSession(c, ctx, cancel)
	s.hello, s.activated, s.admitted = true, true, true
	s.state.ActiveRoles = []Role{Player, Controller}
	s.channel = &wire.Channel{ServerID: c.ClientID(), Category: "lt"}
	c.active = s
	c.setPlayerControlSession(s)
	t.Cleanup(func() { cancel(); c.endPlayerControlSession(s) })
	return c, s, store
}

func waitControl(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("player control condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *session) testControlRequest(kind string, value any) error {
	return s.request(request{ctx: context.Background(), kind: kind, value: value})
}

func TestExternalCapabilitiesAndKnownStateMatrix(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			c, s, store := externalSession(t, PlayerControlConfig{})
			r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: mask&1 != 0, Mute: mask&2 != 0}}
			if mask&4 != 0 {
				r.Volume = controlPointer(0)
			}
			if mask&8 != 0 {
				r.Muted = controlPointer(false)
			}
			var calls atomic.Int32
			target := PlayerControlTarget{Initial: r, SetVolume: func(context.Context, int) error { calls.Add(1); return nil }, SetMuted: func(context.Context, bool) error { calls.Add(1); return nil }}
			binding, err := c.BindPlayerControl(target)
			if err != nil {
				t.Fatal(err)
			}
			state := c.Snapshot()
			if state.VolumeKnown != (r.Volume != nil) || state.MutedKnown != (r.Muted != nil) {
				t.Fatal("invented known state", state)
			}
			want := []string{}
			if r.Capabilities.Volume && r.Volume != nil {
				want = append(want, "volume")
			}
			if r.Capabilities.Mute && r.Muted != nil {
				want = append(want, "mute")
			}
			want = append(want, "set_output_delay")
			if !slices.Equal(playerCommands(state), want) {
				t.Fatal("wrong commands", playerCommands(state), want)
			}
			for _, cmd := range []struct {
				name, kind string
				value      any
			}{{"volume", "volume", 50}, {"mute", "muted", true}} {
				err = s.testControlRequest(cmd.kind, cmd.value)
				if slices.Contains(want, cmd.name) {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, ErrInactiveRole) {
					t.Fatal("read-only/unknown was writable", err)
				}
			}
			waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
			if _, err = c.ReportPlayerControl(binding, r); err != nil {
				t.Fatal(err)
			}
			if store.Saves.Load() != 1 {
				t.Fatal("external request/report persisted observations")
			}
			if c.Snapshot().Volume != 0 || c.Snapshot().Muted {
				t.Fatal("write changed observation")
			}
			if calls.Load() != int32(len(want)-1) {
				t.Fatal("unexpected setter count", calls.Load())
			}
		})
	}
}

func TestExternalQueueFIFOOverloadAndBestEffortEvents(t *testing.T) {
	c, s, store := externalSession(t, PlayerControlConfig{QueueCapacity: 4, RequestTimeout: time.Second})
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var got []int
	_, err := c.BindPlayerControl(PlayerControlTarget{Initial: PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(7), Muted: controlPointer(true)}, SetVolume: func(ctx context.Context, v int) error {
		if v == 10 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		mu.Lock()
		got = append(got, v)
		mu.Unlock()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Saturating Events must never lose an accepted write.
	for range 100 {
		c.emit(Event{Kind: "ignored"})
	}
	if err = s.testControlRequest("volume", 10); err != nil {
		t.Fatal(err)
	}
	<-started
	for _, v := range []int{11, 12, 13} {
		if err = s.testControlRequest("volume", v); err != nil {
			t.Fatal(err)
		}
	}
	for range 1000 {
		if err = s.testControlRequest("volume", 14); !errors.Is(err, ErrPlayerControlOverload) {
			t.Fatal("overload not rejected", err)
		}
	}
	p := c.playerControl
	p.mu.Lock()
	if len(p.queue) != 3 || !p.running {
		t.Fatal("queue not bounded")
	}
	p.mu.Unlock()
	close(release)
	waitControl(t, func() bool { return c.Snapshot().PlayerControl.VolumeRequest.Status == "succeeded" })
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(got, []int{10, 11, 12, 13}) {
		t.Fatal("accepted writes lost/coalesced/reordered", got)
	}
	state := c.Snapshot()
	if state.Volume != 7 || !state.Muted || store.Saves.Load() != 1 {
		t.Fatal("requests changed observations/mute/store")
	}
}

func TestExternalGenerationCancelsAndRejectsStaleCompletions(t *testing.T) {
	for _, change := range []string{"target", "capability", "unknown", "session", "rekey"} {
		t.Run(change, func(t *testing.T) {
			c, s, _ := externalSession(t, PlayerControlConfig{})
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			initial := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(20)}
			binding, err := c.BindPlayerControl(PlayerControlTarget{Initial: initial, SetVolume: func(ctx context.Context, _ int) error {
				calls.Add(1)
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return nil // Deliberately late success.
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.testControlRequest("volume", 50); err != nil {
				t.Fatal(err)
			}
			<-started
			if err = s.testControlRequest("volume", 60); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "target":
				_, err = c.BindPlayerControl(PlayerControlTarget{Initial: PlayerControlReport{Volume: controlPointer(80)}})
			case "capability":
				r := initial
				r.Capabilities.Volume = false
				_, err = c.ReportPlayerControl(binding, r)
			case "unknown":
				r := initial
				r.Volume = nil
				_, err = c.ReportPlayerControl(binding, r)
			case "session":
				c.setPlayerControlSession(newSession(c, context.Background(), func() {}))
			case "rekey":
				c.endPlayerControlSession(s)
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("inflight not canceled promptly")
			}
			if change == "target" || change == "capability" || change == "unknown" {
				if _, err = c.ReportPlayerControl(binding, initial); !errors.Is(err, ErrPlayerControlStale) {
					t.Fatal("stale report accepted", err)
				}
			}
			before := c.Snapshot()
			close(release)
			waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
			if calls.Load() != 1 || !reflect.DeepEqual(before.PlayerControl, c.Snapshot().PlayerControl) {
				t.Fatal("stale completion changed state or queued old write ran")
			}
		})
	}
}

func TestExternalTimeoutFailureAndCallerCancellation(t *testing.T) {
	for _, mode := range []string{"timeout", "failure", "empty-error", "cancel", "cancel-success", "cancel-failure", "cancel-panic", "backend-cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			timeout := time.Minute
			if mode == "timeout" {
				timeout = 100 * time.Millisecond
			}
			c, s, _ := externalSession(t, PlayerControlConfig{RequestTimeout: timeout})
			started := make(chan struct{})
			_, err := c.BindPlayerControl(PlayerControlTarget{Initial: PlayerControlReport{Capabilities: PlayerControlCapabilities{Mute: true}, Muted: controlPointer(false)}, SetMuted: func(ctx context.Context, _ bool) error {
				close(started)
				switch mode {
				case "failure":
					return errors.New("fake backend failure")
				case "empty-error":
					return errors.New("")
				case "panic":
					panic("fake panic")
				case "backend-cancel":
					return context.Canceled // Even with a still-live request context.
				default:
					<-ctx.Done()
					if mode == "cancel-success" {
						return nil
					}
					if mode == "cancel-failure" {
						return errors.New("backend error racing cancellation")
					}
					if mode == "cancel-panic" {
						panic("backend panic racing cancellation")
					}
					return ctx.Err()
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err = s.request(request{ctx: ctx, kind: "muted", value: true}); err != nil {
				t.Fatal(err)
			}
			<-started
			if slices.Contains([]string{"cancel", "cancel-success", "cancel-failure", "cancel-panic"}, mode) {
				cancel()
			}
			waitControl(t, func() bool { return c.Snapshot().PlayerControl.MuteRequest.Status != "pending" })
			state := c.Snapshot()
			r := state.PlayerControl.MuteRequest
			if r.Error == "" || state.Muted || !state.MutedKnown || r.Status == "succeeded" {
				t.Fatal("error confirmed mute", state)
			}
			if s.fatal != nil {
				t.Fatal("backend failure poisoned protocol")
			}
			if state.PlayerControl.Capabilities != (PlayerControlCapabilities{}) || state.PlayerControl.Error == "" {
				t.Fatal("executed failed/uncertain write did not withdraw controls or retain error", state.PlayerControl)
			}
			if slices.Contains([]string{"timeout", "failure", "empty-error", "panic", "cancel-failure", "cancel-panic"}, mode) && r.Status != "failed" {
				t.Fatal("substantive failure overwritten by cancellation", mode, r)
			}
			if slices.Contains([]string{"cancel", "cancel-success", "backend-cancel"}, mode) && r.Status != "canceled" {
				t.Fatal("in-flight cancellation lost its request status", mode, r)
			}
		})
	}
}

func TestExternalExecutedFailureWithdrawsBothControlsUntilHostReport(t *testing.T) {
	for _, kind := range []string{"volume", "muted"} {
		for _, outcome := range []string{"error", "panic"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				input := newFakeSourceInput()
				started, release := make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				var volumeCalls, muteCalls atomic.Int32
				failFirst := func(ctx context.Context, calls *atomic.Int32) error {
					if calls.Add(1) != 1 {
						return nil
					}
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if outcome == "panic" {
						panic("fake host lost")
					}
					return errors.New("fake host lost")
				}
				target := PlayerControlTarget{
					Initial: PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true, Mute: true}, Volume: controlPointer(20), Muted: controlPointer(true)},
					SetVolume: func(ctx context.Context, _ int) error {
						if kind == "volume" {
							return failFirst(ctx, &volumeCalls)
						}
						volumeCalls.Add(1)
						return nil
					},
					SetMuted: func(ctx context.Context, _ bool) error {
						if kind == "muted" {
							return failFirst(ctx, &muteCalls)
						}
						muteCalls.Add(1)
						return nil
					},
				}
				s, p, binding := newPlayerSourceControlSession(t, input, target)
				sourceStart(t, s, p, input)
				capture := s.source.capture
				// All notifications can be dropped; Snapshot must retain the fault.
				for range 100 {
					s.client.emit(Event{Kind: "saturate"})
				}
				value, queuedValue := any(50), any(false)
				queuedKind := "muted"
				if kind == "muted" {
					value, queuedValue, queuedKind = false, 50, "volume"
				}
				if err := s.testControlRequest(kind, value); err != nil {
					t.Fatal(err)
				}
				awaitPlayerSourceControl(t, s, started, "setter was not invoked")
				if err := s.testControlRequest(queuedKind, queuedValue); err != nil {
					t.Fatal(err)
				}
				close(release)
				waitControl(t, func() bool {
					control := s.client.playerControl
					control.mu.Lock()
					defer control.mu.Unlock()
					return !control.running
				})
				state := s.client.Snapshot()
				failed, queued := state.PlayerControl.VolumeRequest, state.PlayerControl.MuteRequest
				calls, otherCalls := volumeCalls.Load(), muteCalls.Load()
				if kind == "muted" {
					failed, queued = queued, failed
					calls, otherCalls = otherCalls, calls
				}
				if failed == nil || failed.Status != "failed" || queued == nil || queued.Status != "canceled" || calls != 1 || otherCalls != 0 {
					t.Fatal("failed request overwritten or queued setter executed", state.PlayerControl, calls, otherCalls)
				}
				if state.PlayerControl.Error == "" || state.PlayerControl.Capabilities != (PlayerControlCapabilities{}) || !slices.Equal(playerCommands(state), []string{"set_output_delay"}) {
					t.Fatal("fault not reliably observable or controls retained", state.PlayerControl)
				}
				if !state.VolumeKnown || !state.MutedKnown || state.Volume != 20 || !state.Muted {
					t.Fatal("failed write invented observations", state)
				}
				assertPlayerSourceCapture(t, s, input, capture, 1)
				if s.fatal != nil || len(s.client.events) != cap(s.client.events) {
					t.Fatal("failure terminated protocol or relied on an Events consumer")
				}
				withdrawn := target.Initial
				withdrawn.Capabilities = PlayerControlCapabilities{}
				if err := s.sendState(); err != nil {
					t.Fatal(err)
				}
				readPlayerSourceControlState(t, p, true, true, true, withdrawn)
				for _, request := range []struct {
					kind  string
					value any
				}{{"volume", 80}, {"muted", false}} {
					if err := s.testControlRequest(request.kind, request.value); !errors.Is(err, ErrInactiveRole) {
						t.Fatal("write accepted without fresh report", err)
					}
				}
				// Even malformed arguments are opaque after supported_commands loss.
				if err := s.serverCommand([]byte(`{"player":{"command":"volume","volume":null,"mute":[]}}`)); err != nil {
					t.Fatal(err)
				}
				if volumeCalls.Load()+muteCalls.Load() != 1 {
					t.Fatal("withdrawn controls reached hardware")
				}
				// Internal withdrawal must NOT secretly rotate the target token.
				restored, err := s.client.ReportPlayerControl(binding, target.Initial)
				if err != nil || restored.generation == nil {
					t.Fatal("original host token cannot restore after setter fault", err)
				}
				if s.client.Snapshot().PlayerControl.Error != "" {
					t.Fatal("fresh report did not clear hardware fault")
				}
				if err := s.sendState(); err != nil {
					t.Fatal(err)
				}
				readPlayerSourceControlState(t, p, true, true, true, target.Initial)
				if err := s.testControlRequest(kind, value); err != nil {
					t.Fatal("restored control cannot accept a write", err)
				}
				waitControl(t, func() bool {
					state := s.client.Snapshot().PlayerControl
					if kind == "volume" {
						return state.VolumeRequest.Status == "succeeded"
					}
					return state.MuteRequest.Status == "succeeded"
				})
				if volumeCalls.Load()+muteCalls.Load() != 2 {
					t.Fatal("queued canceled request resurrected after restore")
				}
				assertPlayerSourceCapture(t, s, input, capture, 1)
				if state := s.client.Snapshot(); state.Volume != 20 || !state.Muted {
					t.Fatal("restored request was treated as observation")
				}
				if err := s.stopSource(); err != nil {
					t.Fatal(err)
				}
				expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
			})
		}
	}
}

func TestExternalCanceledOrExpiredBeforeInvocationDoesNotWithdrawControls(t *testing.T) {
	for _, mode := range []string{"already-canceled", "already-expired", "queued-cancel", "queued-deadline"} {
		t.Run(mode, func(t *testing.T) {
			c, s, _ := externalSession(t, PlayerControlConfig{RequestTimeout: time.Minute})
			started, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var volumeCalls, muteCalls atomic.Int32
			r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true, Mute: true}, Volume: controlPointer(20), Muted: controlPointer(true)}
			_, err := c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(ctx context.Context, v int) error {
				volumeCalls.Add(1)
				if v == 30 {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}, SetMuted: func(context.Context, bool) error { muteCalls.Add(1); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "already-canceled" || mode == "already-expired" {
				ctx, cancel := context.WithCancel(context.Background())
				if mode == "already-expired" {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				}
				cancel()
				err = s.request(request{ctx: ctx, kind: "muted", value: false})
				if !errors.Is(err, ctx.Err()) || muteCalls.Load() != 0 || c.Snapshot().PlayerControl.MuteRequest != nil {
					t.Fatal("pre-canceled request was accepted/invoked", err)
				}
			} else {
				if err := s.testControlRequest("volume", 30); err != nil {
					t.Fatal(err)
				}
				awaitPlayerSourceControl(t, s, started, "first setter did not start")
				ctx, cancel := context.WithCancel(context.Background())
				if mode == "queued-deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				}
				defer cancel()
				if err := s.request(request{ctx: ctx, kind: "muted", value: false}); err != nil {
					t.Fatal(err)
				}
				if mode == "queued-cancel" {
					cancel()
				}
				<-ctx.Done()
				if err := s.testControlRequest("volume", 40); err != nil {
					t.Fatal(err)
				}
				close(release)
				waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
				state := c.Snapshot().PlayerControl
				want := "canceled"
				if mode == "queued-deadline" {
					want = "failed"
				}
				if state.MuteRequest.Status != want || state.VolumeRequest.Status != "succeeded" || volumeCalls.Load() != 2 || muteCalls.Load() != 0 {
					t.Fatal("queued cancellation reached backend or canceled another accepted write", state)
				}
			}
			state := c.Snapshot()
			if state.PlayerControl.Capabilities != r.Capabilities || state.PlayerControl.Error != "" || state.Volume != 20 || !state.Muted {
				t.Fatal("unexecuted request withdrew controls or changed observations", state)
			}
		})
	}
}

func TestExternalStaleSetterFailureCannotWithdrawRestoredControls(t *testing.T) {
	for _, transition := range []string{"target", "host-capability-restore", "session", "session-context"} {
		for _, outcome := range []string{"error", "panic"} {
			t.Run(transition+"/"+outcome, func(t *testing.T) {
				c, s, _ := externalSession(t, PlayerControlConfig{RequestTimeout: time.Minute})
				started, release := make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				var oldCalls, newCalls atomic.Int32
				r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true, Mute: true}, Volume: controlPointer(20), Muted: controlPointer(true)}
				b, err := c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(ctx context.Context, _ int) error {
					if oldCalls.Add(1) != 1 {
						return nil
					}
					close(started)
					<-ctx.Done()
					<-release
					if outcome == "panic" {
						panic("stale host panic")
					}
					return errors.New("stale host failure")
				}, SetMuted: func(context.Context, bool) error { return nil }})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.testControlRequest("volume", 50); err != nil {
					t.Fatal(err)
				}
				awaitPlayerSourceControl(t, s, started, "old setter did not start")
				switch transition {
				case "target":
					_, err = c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(context.Context, int) error { newCalls.Add(1); return nil }, SetMuted: func(context.Context, bool) error { return nil }})
				case "host-capability-restore":
					disabled := r
					disabled.Capabilities = PlayerControlCapabilities{}
					b, err = c.ReportPlayerControl(b, disabled)
					if err == nil {
						_, err = c.ReportPlayerControl(b, r)
					}
				case "session":
					c.endPlayerControlSession(s)
					c.setPlayerControlSession(s)
				case "session-context":
					s.cancel()
					ctx, cancel := context.WithCancel(context.Background())
					t.Cleanup(cancel)
					next := newSession(c, ctx, cancel)
					next.hello, next.activated, next.admitted = true, true, true
					next.state.ActiveRoles = []Role{Player, Controller}
					next.channel = s.channel
					c.mu.Lock()
					c.active = next
					c.mu.Unlock()
					c.setPlayerControlSession(next)
					s = next
					t.Cleanup(func() { c.endPlayerControlSession(next) })
				}
				if err != nil {
					t.Fatal(err)
				}
				before := c.Snapshot().PlayerControl
				close(release)
				waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
				if after := c.Snapshot().PlayerControl; !reflect.DeepEqual(before, after) || after.Capabilities != r.Capabilities || after.Error != "" {
					t.Fatal("stale setter fault damaged restored controls", before, after)
				}
				if err := s.testControlRequest("volume", 70); err != nil {
					t.Fatal("new generation no longer writable", err)
				}
				waitControl(t, func() bool { return c.Snapshot().PlayerControl.VolumeRequest.Status == "succeeded" })
				if oldCalls.Load()+newCalls.Load() != 2 {
					t.Fatal("new generation did not execute exactly one fresh write")
				}
			})
		}
	}
}

func TestExternalSessionCancellationIsNotHardwareFailure(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{})
	started := make(chan struct{})
	r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(20)}
	_, err := c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(ctx context.Context, _ int) error { close(started); <-ctx.Done(); return ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.testControlRequest("volume", 50); err != nil {
		t.Fatal(err)
	}
	awaitPlayerSourceControl(t, s, started, "setter did not start")
	// Deliberately delay the normal epoch-changing session cleanup: even this
	// window must not misclassify session shutdown as a hardware failure.
	s.cancel()
	waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
	if state := c.Snapshot().PlayerControl; state.Capabilities != r.Capabilities || state.Error != "" || state.VolumeRequest.Status != "canceled" {
		t.Fatal("session cancellation withdrew target controls", state)
	}
}

func TestExternalFailureDiagnosticSurvivesNewerQueuedRequestsAndDroppedEvents(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{RequestTimeout: time.Minute})
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(20)}
	b, err := c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(ctx context.Context, _ int) error {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return errors.New("persistent fake backend failure")
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		c.emit(Event{Kind: "saturate"})
	}
	if err := s.testControlRequest("volume", 50); err != nil {
		t.Fatal(err)
	}
	awaitPlayerSourceControl(t, s, started, "setter did not start")
	for _, v := range []int{60, 70} {
		if err := s.testControlRequest("volume", v); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
	state := c.Snapshot()
	if state.PlayerControl.VolumeRequest.Status != "canceled" || *state.PlayerControl.VolumeRequest.Volume != 70 || state.PlayerControl.Error != "persistent fake backend failure" || calls.Load() != 1 || state.Volume != 20 || state.PlayerControl.Capabilities.Volume {
		t.Fatal("queued request hid the hardware error or reached backend", state)
	}
	// Unrelated lifecycle invalidation must not silently clear a target fault.
	c.endPlayerControlSession(s)
	c.setPlayerControlSession(s)
	if state := c.Snapshot().PlayerControl; state.Error != "persistent fake backend failure" || state.Capabilities.Volume {
		t.Fatal("session transition cleared backend fault", state)
	}
	r.Capabilities = PlayerControlCapabilities{}
	if _, err := c.ReportPlayerControl(b, r); err != nil {
		t.Fatal("valid target token could not report read-only recovery", err)
	}
	if state := c.Snapshot().PlayerControl; state.Error != "" || state.Capabilities.Volume || calls.Load() != 1 {
		t.Fatal("read-only host report failed to clear diagnostic or caused a write", state)
	}
}

func TestExternalInvalidConfigurationNeverSelectsLegacy(t *testing.T) {
	for _, cfg := range []PlayerControlConfig{{QueueCapacity: -1}, {QueueCapacity: 257}, {RequestTimeout: -1}, {RequestTimeout: time.Hour}} {
		store := &controlCountingStore{}
		c, err := New(context.Background(), Config{Name: "invalid", Store: store, Roles: []Role{Controller}, PlayerControl: &cfg})
		if err == nil || c != nil || store.Saves.Load() != 0 {
			t.Fatal("invalid external config selected legacy or wrote store")
		}
	}
	c, s, _ := externalSession(t, PlayerControlConfig{})
	if _, err := c.BindPlayerControl(PlayerControlTarget{Initial: PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}}}); err == nil {
		t.Fatal("missing setter accepted")
	}
	if err := s.testControlRequest("volume", 40); !errors.Is(err, ErrInactiveRole) {
		t.Fatal("invalid external fell back to legacy", err)
	}
	if c.Snapshot().VolumeKnown || c.Snapshot().MutedKnown {
		t.Fatal("loaded legacy state as external observation")
	}
}

func TestExternalUnadvertisedDiscriminatorIgnoresMalformedArguments(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{})
	_, err := c.BindPlayerControl(PlayerControlTarget{Initial: PlayerControlReport{Volume: controlPointer(33)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"player":{"command":"volume","volume":null,"mute":[],"output_delay_ms":{}}}`, `{"player":{"command":"mute","mute":42}}`} {
		if err = s.serverCommand([]byte(raw)); err != nil {
			t.Fatal("unoffered arguments validated", err)
		}
	}
	for _, raw := range []string{`{"player":{"command":null}}`, `{"player":{"command":"set_output_delay","output_delay_ms":null}}`} {
		if err = s.serverCommand([]byte(raw)); !errors.Is(err, ErrProtocol) {
			t.Fatal("malformed offered command accepted", err)
		}
	}
}

func TestExternalWireReadOnlyUnknownLiveLossAndReactivation(t *testing.T) {
	cfg := schemaConfig()
	cfg.PlayerControl = &PlayerControlConfig{}
	s, peer := newSchemaSession(t, cfg)
	s.hello, s.activated = true, true
	s.state.ActiveRoles = []Role{Player}
	s.active = map[string]Role{"player": Player}
	s.state.OutputDelayMS = 41
	s.client.setPlayerControlSession(s)
	var writes atomic.Int32
	r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true, Mute: true}, Volume: controlPointer(0)}
	binding, err := s.client.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(context.Context, int) error { writes.Add(1); return nil }, SetMuted: func(context.Context, bool) error { writes.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantCommands []string, volume *int, muted *bool) {
		t.Helper()
		if err := s.sendState(); err != nil {
			t.Fatal(err)
		}
		kind, raw, err := peer.readSecure()
		if err != nil || kind != "client/state" {
			t.Fatal(kind, err)
		}
		var p struct {
			Player map[string]any `json:"player"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		commands := make([]any, 0, len(wantCommands))
		for _, v := range wantCommands {
			commands = append(commands, v)
		}
		if !reflect.DeepEqual(p.Player["supported_commands"], commands) {
			t.Fatal("wrong wire capabilities", p.Player)
		}
		v, vok := p.Player["volume"]
		m, mok := p.Player["muted"]
		if vok != (volume != nil) || mok != (muted != nil) || volume != nil && v != float64(*volume) || muted != nil && m != *muted {
			t.Fatal("invented/stale optional state", p.Player)
		}
		if p.Player["output_delay_ms"] != float64(41) {
			t.Fatal("delay lost")
		}
	}
	check([]string{"volume", "set_output_delay"}, r.Volume, nil)
	old := binding
	r.Capabilities = PlayerControlCapabilities{}
	r.Muted = controlPointer(true)
	binding, err = s.client.ReportPlayerControl(binding, r)
	if err != nil || binding == old {
		t.Fatal("capability generation did not change", err)
	}
	check([]string{"set_output_delay"}, r.Volume, r.Muted)
	r = PlayerControlReport{Err: errors.New("fake target unavailable")}
	binding, err = s.client.ReportPlayerControl(binding, r)
	if err != nil {
		t.Fatal(err)
	}
	check([]string{"set_output_delay"}, nil, nil)
	if s.client.Snapshot().PlayerControl.Error == "" {
		t.Fatal("nonterminal backend diagnostic missing")
	}
	r = PlayerControlReport{Capabilities: PlayerControlCapabilities{Mute: true}, Muted: controlPointer(false)}
	_, err = s.client.ReportPlayerControl(binding, r)
	if err != nil {
		t.Fatal(err)
	}
	check([]string{"mute", "set_output_delay"}, nil, r.Muted)
	if err = s.activate([]byte(`{"activities":[],"active_roles":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.activate([]byte(`{"activities":[],"active_roles":["player@v1"]}`)); err != nil {
		t.Fatal(err)
	}
	// Activation sends a fresh coherent state, not stale software credentials.
	kind, raw, err := peer.readSecure()
	if err != nil || kind != "client/state" {
		t.Fatal(kind, err)
	}
	var p struct {
		Player map[string]any `json:"player"`
	}
	_ = json.Unmarshal(raw, &p)
	if _, ok := p.Player["volume"]; ok || p.Player["muted"] != false {
		t.Fatal("reactivation sent stale state", p.Player)
	}
	if writes.Load() != 0 {
		t.Fatal("startup, readback, target/activation change wrote hardware")
	}
}

func TestExternalLocalSettersUsePlayerPathNotGroupCommand(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{})
	var volumes, mutes atomic.Int32
	_, err := c.BindPlayerControl(PlayerControlTarget{Initial: PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true, Mute: true}, Volume: controlPointer(4), Muted: controlPointer(true)}, SetVolume: func(context.Context, int) error { volumes.Add(1); return nil }, SetMuted: func(context.Context, bool) error { mutes.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-s.ctx.Done():
				return
			case r := <-s.requests:
				r.done <- s.request(r)
			}
		}
	}()
	t.Cleanup(func() { s.cancel(); <-done })
	if err = c.SetVolume(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if err = c.SetMuted(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// No controller state was supplied: group controls remain unavailable and
	// cannot accidentally use local player setters (there is no live socket).
	if err = c.Command(context.Background(), Command{Command: "volume", Volume: controlPointer(25)}); !errors.Is(err, ErrInactiveRole) {
		t.Fatal("group command entered player path", err)
	}
	if err = s.serverCommand([]byte(`{"player":{"command":"volume","volume":60}}`)); err != nil {
		t.Fatal(err)
	}
	waitControl(t, func() bool { return volumes.Load() == 2 && mutes.Load() == 1 })
	state := c.Snapshot()
	if state.Volume != 4 || !state.Muted {
		t.Fatal("request treated as readback/unmuted")
	}
}

func TestExternalConcurrentReadbackSnapshotAndRequests(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{QueueCapacity: 256})
	r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(20)}
	b, err := c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(context.Context, int) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Go(func() {
			for i := 0; i < 100; i++ {
				_, err := c.ReportPlayerControl(b, r)
				if err != nil {
					t.Error(err)
				}
				state := c.Snapshot()
				if state.PlayerControl.VolumeRequest != nil {
					state.PlayerControl.VolumeRequest.Status = "caller-owned"
				}
				err = c.playerControl.enqueue(s, request{ctx: context.Background(), kind: "volume", value: i % 101})
				if err != nil && !errors.Is(err, ErrPlayerControlOverload) {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
}

// A fake timed output, not a physical clock or synchronization measurement.
type controlAudioOutput struct{ writes chan []float32 }
type controlAudioDevice struct {
	writes chan []float32
	next   time.Time
	format audio.Format
}

func (o controlAudioOutput) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	return &controlAudioDevice{writes: o.writes, next: time.Now(), format: f}, ctx.Err()
}
func (d *controlAudioDevice) Next(ctx context.Context) (time.Time, error) { return d.next, ctx.Err() }
func (d *controlAudioDevice) Write(ctx context.Context, v []float32) error {
	select {
	case d.writes <- slices.Clone(v):
		d.next = d.next.Add(time.Duration(len(v)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *controlAudioDevice) Flush(context.Context) error { d.next = time.Now(); return nil }
func (*controlAudioDevice) Close() error                  { return nil }

func TestExternalAndFixedProductionSessionUnityAndSlowBackendIsolation(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, volume := range []int{0, 1, 50, 100} {
			for _, muted := range []bool{false, true} {
				t.Run(fmt.Sprintf("fixed=%v/volume=%d/muted=%v", fixed, volume, muted), func(t *testing.T) {
					writes := make(chan []float32, 8)
					cfg := schemaConfig()
					cfg.Roles = []Role{Player}
					cfg.PlayerControl = &PlayerControlConfig{QueueCapacity: 2}
					cfg.Output = controlAudioOutput{writes: writes}
					cfg.Formats = []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}}
					s, peer := newSchemaSession(t, cfg)
					s.hello, s.activated = true, true
					s.state.ActiveRoles = []Role{Player}
					s.active = map[string]Role{"player": Player}
					s.state.OutputDelayMS = 41
					s.state.Available = true
					for i := int64(1); i <= 4; i++ {
						s.filter.Update(0, 1, i*1000)
					}
					started := make(chan struct{})
					target := PlayerControlTarget{Initial: PlayerControlReport{Volume: &volume, Muted: &muted}}
					if !fixed {
						target.Initial.Capabilities.Volume = true
						target.SetVolume = func(ctx context.Context, _ int) error { close(started); <-ctx.Done(); return ctx.Err() }
					}
					if _, err := s.client.BindPlayerControl(target); err != nil {
						t.Fatal(err)
					}
					s.client.setPlayerControlSession(s)
					var timeRequests atomic.Int32
					peerDone := make(chan struct{})
					go func() {
						defer close(peerDone)
						for {
							kind, _, err := peer.readSecure()
							if err != nil {
								return
							}
							if kind == "client/time" {
								timeRequests.Add(1)
							}
						}
					}()
					runDone := make(chan error, 1)
					go func() { runDone <- s.run() }()
					t.Cleanup(func() {
						// The schema fixture shares s.ctx with the peer's HTTP handler.
						// Let the actor finish before canceling that context, otherwise
						// peer closure races shutdown and can replace its result with EOF.
						defer s.cancel()
						s.displace <- "shutdown"
						select {
						case err := <-runDone:
							var goodbye *GoodbyeError
							if !errors.As(err, &goodbye) || goodbye.Reason != "shutdown" {
								t.Error("session exited unexpectedly", err)
							}
						case <-time.After(3 * time.Second):
							t.Error("session did not shut down")
							s.cancel()
						}
						<-peerDone
					})
					if !fixed {
						if err := peer.sendSecure([]byte(`{"type":"server/command","payload":{"player":{"command":"volume","volume":50}}}`)); err != nil {
							t.Fatal(err)
						}
						select {
						case <-started:
						case <-time.After(time.Second):
							t.Fatal("setter not dispatched")
						}
						// New server writes overflow the bounded queue without killing it.
						for range 20 {
							if err := peer.sendSecure([]byte(`{"type":"server/command","payload":{"player":{"command":"volume","volume":60}}}`)); err != nil {
								t.Fatal(err)
							}
						}
					}
					if err := peer.sendSecure([]byte(`{"type":"group/update","payload":{"group_id":"responsive","group_name":"test","playback_state":"playing"}}`)); err != nil {
						t.Fatal(err)
					}
					if err := peer.sendSecure([]byte(`{"type":"stream/start","payload":{"server_transmitted":1,"player":{"codec":"pcm","sample_rate":8000,"channels":1,"bit_depth":16}}}`)); err != nil {
						t.Fatal(err)
					}
					// Real receive/decode/engine path, with old persisted software volume
					// 73 + mute=true from newSchemaSession still in the credential store.
					plain := make([]byte, 13+320*2)
					plain[0] = 4
					binary.BigEndian.PutUint64(plain[1:9], uint64(s.now()+150000))
					for i := 13; i < len(plain); i += 2 {
						binary.LittleEndian.PutUint16(plain[i:], 8192)
					}
					packet, err := peer.out.Encrypt(nil, nil, plain)
					if err != nil {
						t.Fatal(err)
					}
					if err = peer.conn.Write(peer.ctx, websocket.MessageBinary, packet); err != nil {
						t.Fatal(err)
					}
					waitControl(t, func() bool { return s.client.Snapshot().Group.ID == "responsive" && timeRequests.Load() > 0 })
					deadline := time.NewTimer(time.Second)
					defer deadline.Stop()
					for {
						select {
						case samples := <-writes:
							var nonzero bool
							for _, v := range samples {
								if v != 0 {
									nonzero = true
									if v != 0.25 {
										t.Fatalf("software attenuation/ramp in external mode: %g", v)
									}
								}
							}
							if nonzero {
								return
							}
						case <-deadline.C:
							t.Fatal("audio blocked by host setter or muted by software")
						}
					}
				})
			}
		}
	}
}

func TestExternalUncooperativeAdapterNeverSpawnsReplacementWorkers(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{QueueCapacity: 2, RequestTimeout: 10 * time.Millisecond})
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(10)}
	_, err := c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(context.Context, int) error { calls.Add(1); close(started); <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.testControlRequest("volume", 20); err != nil {
		t.Fatal(err)
	}
	<-started
	// Even a contract-violating host cannot grow workers through target storms.
	for range 100 {
		_, err = c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(ctx context.Context, _ int) error { calls.Add(1); return ctx.Err() }})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.testControlRequest("volume", 30); err != nil {
			t.Fatal(err)
		}
		p := c.playerControl
		p.mu.Lock()
		bounded := p.running && p.inflight != nil && len(p.queue) == 1
		p.mu.Unlock()
		if !bounded || calls.Load() != 1 {
			t.Fatal("spawned replacement worker or unbounded queue")
		}
	}
	c.endPlayerControlSession(s)
	close(release)
	waitControl(t, func() bool { p := c.playerControl; p.mu.Lock(); defer p.mu.Unlock(); return !p.running })
	if calls.Load() != 1 {
		t.Fatal("stale queued writes ran after worker was unblocked")
	}
}

func TestExternalRepeatedActivationOwnerDoesNotCancelAcceptedWrites(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{})
	started, release := make(chan struct{}), make(chan struct{})
	_, err := c.BindPlayerControl(PlayerControlTarget{Initial: PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: controlPointer(10)}, SetVolume: func(ctx context.Context, _ int) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.testControlRequest("volume", 20); err != nil {
		t.Fatal(err)
	}
	<-started
	for range 10 {
		c.setPlayerControlSession(s)
	} // Same session/role, no generation change.
	if got := c.Snapshot().PlayerControl.VolumeRequest.Status; got != "pending" {
		t.Fatal("unchanged activation canceled accepted write", got)
	}
	close(release)
	waitControl(t, func() bool { return c.Snapshot().PlayerControl.VolumeRequest.Status == "succeeded" })
}

type controlSnapshotError struct{ client *Client }

func (e controlSnapshotError) Error() string {
	_ = e.client.Snapshot()
	return "fake reentrant diagnostic"
}

func TestExternalHostErrorFormattingOutsideLocksAndOwnedReadback(t *testing.T) {
	c, s, _ := externalSession(t, PlayerControlConfig{})
	v, m := 20, true
	r := PlayerControlReport{Capabilities: PlayerControlCapabilities{Volume: true}, Volume: &v, Muted: &m, Err: controlSnapshotError{c}}
	b, err := c.BindPlayerControl(PlayerControlTarget{Initial: r, SetVolume: func(context.Context, int) error { return controlSnapshotError{c} }})
	if err != nil {
		t.Fatal(err)
	}
	v, m = 99, false
	if state := c.Snapshot(); state.Volume != 20 || !state.Muted {
		t.Fatal("binding retained caller-owned observation pointers")
	}
	if err = s.testControlRequest("volume", 30); err != nil {
		t.Fatal(err)
	}
	waitControl(t, func() bool { return c.Snapshot().PlayerControl.VolumeRequest.Status == "failed" })
	b, err = c.ReportPlayerControl(b, r)
	if err != nil {
		t.Fatal(err)
	}
	v = 1
	if state := c.Snapshot(); state.Volume != 99 || state.Muted {
		t.Fatal("report retained caller-owned observation pointers")
	}
	if b.generation == nil {
		t.Fatal("binding lost")
	}
}
