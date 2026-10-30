package sendspin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

type sourceRead struct {
	samples []float32
	first   time.Time
	err     error
}
type fakeSourceInput struct {
	opens   atomic.Int32
	closes  atomic.Int32
	reads   chan sourceRead
	gate    <-chan struct{}
	late    bool
	openErr error
}
type fakeSourceDevice struct{ input *fakeSourceInput }

func newFakeSourceInput() *fakeSourceInput {
	return &fakeSourceInput{reads: make(chan sourceRead, 128)}
}
func (f *fakeSourceInput) Open(ctx context.Context, _ audio.Format) (audio.InputDevice, error) {
	f.opens.Add(1)
	if f.gate != nil {
		if f.late {
			<-f.gate
		} else {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.gate:
			}
		}
	}
	if f.openErr != nil {
		return nil, f.openErr
	}
	return &fakeSourceDevice{input: f}, nil
}
func (d *fakeSourceDevice) Read(ctx context.Context, dst []float32) (int, time.Time, error) {
	select {
	case <-ctx.Done():
		return 0, time.Time{}, ctx.Err()
	case r := <-d.input.reads:
		if len(r.samples) > len(dst) {
			return len(r.samples), r.first, r.err
		}
		return copy(dst, r.samples), r.first, r.err
	}
}
func (d *fakeSourceDevice) Close() error { d.input.closes.Add(1); return nil }

func sourceWait(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("source condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func sourceReady(s *session) {
	for i := int64(1); i <= 8; i++ {
		s.filter.Update(1_000_000_000+i*100, 10, i*1_000_000)
	}
}
func newSourceSchema(t *testing.T, input *fakeSourceInput, suite ...string) (*session, *handshakePeer) {
	t.Helper()
	cfg := Config{Name: "Source fixture", Roles: []Role{SourceRole}, Input: input, SourceFormat: audio.Format{SampleRate: 8000, Channels: 1}}
	if len(suite) != 0 {
		cfg.CipherSuite = suite[0]
	}
	s, p := newSchemaSession(t, cfg)
	t.Cleanup(s.closeSource)
	schemaReceive(t, s, p, "server/hello", map[string]any{"name": "Source server", "source@v1_support": map[string]any{"supported_codecs": []string{"future", "flac", "pcm"}}})
	kind, raw, err := p.readSecure()
	if err != nil || kind != "client/hello" {
		t.Fatal(kind, err)
	}
	var hello map[string]json.RawMessage
	_ = json.Unmarshal(raw, &hello)
	if string(hello["source@v1_support"]) != "{}" {
		t.Fatal("missing/minvented source support", string(raw))
	}
	if input.opens.Load() != 0 {
		t.Fatal("hello opened input")
	}
	sourceReady(s)
	schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{SourceRole}})
	expectSchemaPayload(t, p, "client/state", map[string]any{"available": true, "source": map[string]any{}})
	if input.opens.Load() != 0 {
		t.Fatal("activation opened input")
	}
	return s, p
}
func sourceStart(t *testing.T, s *session, p *handshakePeer, input *fakeSourceInput) {
	t.Helper()
	schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	sourceWait(t, func() bool {
		select {
		case <-s.source.capture.ready:
			return true
		default:
			return false
		}
	})
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/start", map[string]any{"source": map[string]any{"codec": "pcm", "sample_rate": float64(8000), "channels": float64(1), "bit_depth": float64(16)}})
}
func sourceQueueLen(c *sourceCapture) int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.queue) }
func sourceAudio(t *testing.T, p *handshakePeer) []byte {
	t.Helper()
	typ, packet, err := p.readRaw()
	if err != nil || typ != websocket.MessageBinary {
		t.Fatal("unencrypted source audio", err)
	}
	plain, err := p.in.Decrypt(nil, nil, packet)
	if err != nil || len(plain) < 9 || plain[0] != 12 {
		t.Fatal("invalid encrypted source audio", err)
	}
	return plain
}
func TestSourceConfigurationIsExplicitAndInert(t *testing.T) {
	for _, tc := range []struct {
		roles  []Role
		input  audio.Input
		format audio.Format
		valid  bool
	}{
		{[]Role{SourceRole}, nil, audio.Format{}, false},
		{[]Role{SourceRole}, newFakeSourceInput(), audio.Format{}, true},
		{[]Role{}, newFakeSourceInput(), audio.Format{}, true},
		{[]Role{SourceRole}, newFakeSourceInput(), audio.Format{SampleRate: 48000}, false},
		{[]Role{SourceRole}, newFakeSourceInput(), audio.Format{SampleRate: 192000, Channels: 8}, true},
		{[]Role{SourceRole}, newFakeSourceInput(), audio.Format{SampleRate: 192001, Channels: 1}, false},
		{[]Role{SourceRole, SourceRole}, newFakeSourceInput(), audio.Format{}, false},
	} {
		_, err := New(context.Background(), Config{Name: "Input", Store: &MemoryStore{}, Roles: tc.roles, Input: tc.input, SourceFormat: tc.format})
		if (err == nil) != tc.valid {
			t.Fatal("configuration validation", tc, err)
		}
		if f, ok := tc.input.(*fakeSourceInput); ok && f.opens.Load() != 0 {
			t.Fatal("New initialized capture")
		}
	}
}
func TestSourceHelloRequiresBaselineSupportAndIgnoresUnknownCodecs(t *testing.T) {
	for _, raw := range []string{`{}`, `{"source@v1_support":null}`, `{"source@v1_support":{}}`, `{"source@v1_support":{"supported_codecs":[]}}`, `{"source@v1_support":{"supported_codecs":["opus"]}}`, `{"source@v1_support":{"supported_codecs":["pcm"]}}`, `{"source@v1_support":{"supported_codecs":["pcm","flac",null]}}`} {
		s := newSession(&Client{cfg: Config{Roles: []Role{SourceRole}}}, context.Background(), func() {})
		if err := s.sourceHello([]byte(raw)); !errors.Is(err, protocol.ErrProtocol) || s.source.supported {
			t.Fatal("invalid source hello accepted", raw, err)
		}
	}
	s := newSession(&Client{cfg: Config{Roles: []Role{SourceRole}}}, context.Background(), func() {})
	if err := s.sourceHello([]byte(`{"source@v1_support":{"supported_codecs":["future","pcm","flac"],"future":false}}`)); err != nil || !s.source.supported {
		t.Fatal(err)
	}
}
func TestSourceStartAudioEndOrderingAndCaptureTimestamp(t *testing.T) {
	f := newFakeSourceInput()
	s, p := newSourceSchema(t, f)
	sourceStart(t, s, p, f)
	for range 3 {
		schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	}
	first := time.Now().Add(-15 * time.Millisecond)
	samples := make([]float32, 160)
	values := []float32{-2, -1, -0.5, 0, 0.5, 1, 2, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))}
	copy(samples, values)
	f.reads <- sourceRead{samples: samples, first: first}
	sourceWait(t, func() bool { return sourceQueueLen(s.source.capture) == 1 })
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	frame := sourceAudio(t, p)
	want, _ := s.filter.CheckedServerTime(first.Sub(s.epoch).Microseconds() + 1)
	if int64(binary.BigEndian.Uint64(frame[1:9])) != want || len(frame) != 329 {
		t.Fatal("timestamp used arrival/offset-only or invalid frame size")
	}
	for i, want := range []int16{-32768, -32768, -16384, 0, 16384, 32767, 32767, 0, 32767, -32768} {
		if int16(binary.LittleEndian.Uint16(frame[9+2*i:])) != want {
			t.Fatal("PCM16 conversion", i)
		}
	}
	schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "stop"}})
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	sourceWait(t, func() bool { return f.closes.Load() == 1 })
	if f.opens.Load() != 1 || s.source.pending || s.source.open || s.streams["source"] {
		t.Fatal("duplicate start reopened or stop retained authorization")
	}
}

func TestSourceFreshChunksWithDelayedADCTimestampsAreSent(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		t.Run(suite, func(t *testing.T) {
			f := newFakeSourceInput()
			s, p := newSourceSchema(t, f, suite)
			sourceStart(t, s, p, f)
			c := s.source.capture
			for i := range 3 {
				readAt := monotime.Now()
				first := readAt.Add(-150 * time.Millisecond)
				samples := make([]float32, 160)
				samples[0] = float32(i+1) / 32768
				f.reads <- sourceRead{samples: samples, first: first}
				sourceWait(t, func() bool { return sourceQueueLen(c) == 1 })
				c.mu.Lock()
				queued := c.queue[0]
				c.mu.Unlock()
				if queued.first != first || queued.queuedAt.Before(readAt) {
					t.Fatal("capture and application enqueue clocks were conflated")
				}
				if err := s.advanceSource(); err != nil {
					t.Fatal(err)
				}
				frame := sourceAudio(t, p)
				want, ok := s.filter.CheckedServerTime(first.Sub(s.epoch).Microseconds() + 1)
				if !ok || int64(binary.BigEndian.Uint64(frame[1:9])) != want || int16(binary.LittleEndian.Uint16(frame[9:])) != int16(i+1) {
					t.Fatal("fresh delayed capture was dropped, reordered or retimestamped")
				}
			}
			if err := s.stopSource(); err != nil {
				t.Fatal(err)
			}
			expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
			sourceWait(t, func() bool { return f.closes.Load() == 1 })
			if f.opens.Load() != 1 || s.source.pending || s.source.open {
				t.Fatal("latency handling changed authorization or stop semantics")
			}
		})
	}
}

func TestSourceQueueStallDropsOldChunksNotFreshDelayedCapture(t *testing.T) {
	f := newFakeSourceInput()
	s, p := newSourceSchema(t, f)
	sourceStart(t, s, p, f)
	c := s.source.capture
	for range 3 {
		samples := make([]float32, 160)
		samples[0] = -0.5 // Identifies any wrongly transmitted stale block.
		f.reads <- sourceRead{samples: samples, first: monotime.Now().Add(-150 * time.Millisecond)}
	}
	sourceWait(t, func() bool { return sourceQueueLen(c) == 3 })
	// Actually hold the actor while capture's bounded queue waits. Do not age
	// ADC timestamps or manufacture enqueue times to simulate application delay.
	sourceWait(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return monotime.Since(c.queue[len(c.queue)-1].queuedAt) > 100*time.Millisecond
	})
	first := monotime.Now().Add(-150 * time.Millisecond)
	samples := make([]float32, 160)
	samples[0] = 0.25
	f.reads <- sourceRead{samples: samples, first: first}
	sourceWait(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.queue) == 3 && c.queue[2].first.Equal(first)
	})
	// Enqueuing fresh audio drops one oldest block; the two remaining stale
	// blocks must be discarded, not burst before the fresh delayed-ADC block.
	for range 3 {
		if err := s.advanceSource(); err != nil {
			t.Fatal(err)
		}
	}
	frame := sourceAudio(t, p)
	want, ok := s.filter.CheckedServerTime(first.Sub(s.epoch).Microseconds() + 1)
	if !ok || int64(binary.BigEndian.Uint64(frame[1:9])) != want || int16(binary.LittleEndian.Uint16(frame[9:])) != 8192 {
		t.Fatal("stalled queue burst stale audio or discarded/retimestamped fresh delayed capture")
	}
	if sourceQueueLen(c) != 0 || f.opens.Load() != 1 || !s.source.open {
		t.Fatal("backlog recovery reopened capture or retained stale blocks")
	}
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	// End must follow the one fresh audio message immediately: any leftover
	// stale wire audio makes this JSON expectation fail as well.
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
	sourceWait(t, func() bool { return f.closes.Load() == 1 })
}

func TestSourcePendingStopLateOpenAndNewAuthorizationAreBounded(t *testing.T) {
	gate := make(chan struct{})
	f := newFakeSourceInput()
	f.gate = gate
	f.late = true
	s, p := newSourceSchema(t, f)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	for range 3 {
		schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	}
	sourceWait(t, func() bool { return f.opens.Load() == 1 })
	old := s.source.capture
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	if old.cancel == nil || old == nil || s.source.pending || s.source.open {
		t.Fatal("pending stop failed")
	}
	// A new authorization may wait, but must never create a second Open worker.
	schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	if f.opens.Load() != 1 || !s.source.pending {
		t.Fatal("unbounded late Open workers")
	}
	close(gate)
	sourceWait(t, func() bool {
		select {
		case <-old.done:
			return true
		default:
			return false
		}
	})
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	sourceWait(t, func() bool {
		select {
		case <-s.source.capture.ready:
			return true
		default:
			return false
		}
	})
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/start", map[string]any{"source": map[string]any{"codec": "pcm", "sample_rate": float64(8000), "channels": float64(1), "bit_depth": float64(16)}})
	if f.opens.Load() != 2 || f.closes.Load() != 1 {
		t.Fatal("late device leaked or new authorization lost")
	}
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
}

func TestSourcePendingAuthorizationClearsOnUnavailableOrRoleLoss(t *testing.T) {
	for _, transition := range []string{"unavailable", "role-loss"} {
		t.Run(transition, func(t *testing.T) {
			gate := make(chan struct{})
			f := newFakeSourceInput()
			f.gate = gate
			s, p := newSourceSchema(t, f)
			schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
			sourceWait(t, func() bool { return f.opens.Load() == 1 })
			old := s.source.capture
			if transition == "unavailable" {
				if err := s.request(request{ctx: s.ctx, kind: "available", value: false}); err != nil {
					t.Fatal(err)
				}
				expectSchemaPayload(t, p, "client/state", map[string]any{"available": false, "source": map[string]any{}})
				if err := s.request(request{ctx: s.ctx, kind: "available", value: true}); err != nil {
					t.Fatal(err)
				}
				expectSchemaPayload(t, p, "client/state", map[string]any{"available": true, "source": map[string]any{}})
			} else {
				schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{}})
				schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{SourceRole}})
				expectSchemaPayload(t, p, "client/state", map[string]any{"available": true, "source": map[string]any{}})
			}
			sourceWait(t, func() bool {
				select {
				case <-old.done:
					return true
				default:
					return false
				}
			})
			close(gate)
			if err := s.advanceSource(); err != nil {
				t.Fatal(err)
			}
			if s.source.pending || s.source.open || s.source.capture != nil || f.opens.Load() != 1 || f.closes.Load() != 0 {
				t.Fatal("canceled pending authorization survived transition")
			}
		})
	}
}

func TestSourceOpenFailureConsumesAuthorizationAndDoesNotSendEnd(t *testing.T) {
	f := newFakeSourceInput()
	f.openErr = io.ErrUnexpectedEOF
	s, p := newSourceSchema(t, f)
	schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
	c := s.source.capture
	sourceWait(t, func() bool {
		select {
		case <-c.done:
			return true
		default:
			return false
		}
	})
	if err := s.advanceSource(); err != nil {
		t.Fatal(err)
	}
	if s.source.pending || s.source.open || s.source.capture != nil || f.opens.Load() != 1 {
		t.Fatal("failed Open retained authorization")
	}
	for range 3 {
		if err := s.advanceSource(); err != nil {
			t.Fatal(err)
		}
	}
	// With no emitted start/end, this explicit state is the next wire message.
	if err := s.sendState(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client/state", map[string]any{"available": true, "source": map[string]any{}})
	f.openErr = nil
	sourceStart(t, s, p, f)
	if f.opens.Load() != 2 {
		t.Fatal("new explicit authorization not usable after failure")
	}
	if err := s.stopSource(); err != nil {
		t.Fatal(err)
	}
	expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
}

func TestSourceCombinedMalformedCommandCannotInitializeInput(t *testing.T) {
	f := newFakeSourceInput()
	s, _ := newSourceSchema(t, f)
	if err := s.serverCommand([]byte(`{"source":{"command":"start"},"player":{"command":"volume","volume":50}}`)); !errors.Is(err, protocol.ErrProtocol) {
		t.Fatal("inactive player command accepted", err)
	}
	if f.opens.Load() != 0 || s.source.pending || s.source.capture != nil {
		t.Fatal("invalid combined command initialized capture")
	}
}

func TestSourceActualCaptureBackpressureDropsOldBlocksWithoutReopening(t *testing.T) {
	f := newFakeSourceInput()
	first := time.Now()
	for i := range 64 {
		samples := make([]float32, 160)
		for j := range samples {
			samples[j] = float32(i) / 32768
		}
		f.reads <- sourceRead{samples: samples, first: first.Add(time.Duration(i) * 20 * time.Millisecond)}
	}
	f.reads <- sourceRead{err: io.EOF}
	c := newSourceCapture(context.Background(), f, audio.Format{SampleRate: 8000, Channels: 1}, make(chan struct{}, 1))
	select {
	case <-c.done:
	case <-time.After(time.Second):
		c.cancel()
		t.Fatal("bounded worker stalled behind full queue")
	}
	if sourceQueueLen(c) != 3 || f.opens.Load() != 1 || f.closes.Load() != 1 {
		t.Fatal("capture queue/workers were not bounded")
	}
	for _, want := range []int16{61, 62, 63} {
		chunk, ok := c.pop()
		if !ok || len(chunk.pcm) != 320 || int16(binary.LittleEndian.Uint16(chunk.pcm)) != want {
			t.Fatal("backpressure did not resume newest capture", want)
		}
	}
}
func TestSourceUnavailableRoleRemovalAndReactivationNeverAutostart(t *testing.T) {
	for _, transition := range []string{"unavailable", "role-removal"} {
		t.Run(transition, func(t *testing.T) {
			f := newFakeSourceInput()
			s, p := newSourceSchema(t, f)
			sourceStart(t, s, p, f)
			if transition == "unavailable" {
				if err := s.request(request{ctx: s.ctx, kind: "available", value: false}); err != nil {
					t.Fatal(err)
				}
				expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
				expectSchemaPayload(t, p, "client/state", map[string]any{"available": false, "source": map[string]any{}})
				schemaReceive(t, s, p, "server/command", map[string]any{"source": map[string]any{"command": "start"}})
				if err := s.request(request{ctx: s.ctx, kind: "available", value: true}); err != nil {
					t.Fatal(err)
				}
				expectSchemaPayload(t, p, "client/state", map[string]any{"available": true, "source": map[string]any{}})
			} else {
				schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{}})
				expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
				schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}, "active_roles": []Role{SourceRole}})
				expectSchemaPayload(t, p, "client/state", map[string]any{"available": true, "source": map[string]any{}})
			}
			sourceWait(t, func() bool { return f.closes.Load() == 1 })
			for range 3 {
				if err := s.advanceSource(); err != nil {
					t.Fatal(err)
				}
			}
			if f.opens.Load() != 1 || s.source.pending || s.source.open {
				t.Fatal("transition automatically resumed recording")
			}
		})
	}
}
func TestSourceDeniedStartsDoNotOpenOrBecomePending(t *testing.T) {
	for _, phase := range []string{"unavailable", "inactive", "unconverged", "policy", "rekey", "unsupported"} {
		t.Run(phase, func(t *testing.T) {
			f := newFakeSourceInput()
			s, _ := newSourceSchema(t, f)
			switch phase {
			case "unavailable":
				s.state.Available = false
			case "inactive":
				s.state.ActiveRoles = nil
			case "unconverged":
				s.filter = newSession(&Client{}, s.ctx, func() {}).filter
			case "policy":
				s.channel.Category = "sn"
			case "rekey":
				s.awaitingActivation = true
			case "unsupported":
				s.source.supported = false
			}
			if err := s.sourceCommand(json.RawMessage(`{"command":"start"}`)); err != nil {
				t.Fatal(err)
			}
			if f.opens.Load() != 0 || s.source.pending || s.source.capture != nil {
				t.Fatal("denied start authorized capture")
			}
		})
	}
}
func TestSourceMalformedCommandsDoNotOpen(t *testing.T) {
	f := newFakeSourceInput()
	s, _ := newSourceSchema(t, f)
	for _, raw := range []string{`null`, `[]`, `{}`, `{"command":null}`, `{"command":true}`, `{"command":"START"}`, `{"command":"future"}`} {
		if err := s.sourceCommand(json.RawMessage(raw)); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal(raw, err)
		}
	}
	if f.opens.Load() != 0 {
		t.Fatal("malformed command opened capture")
	}
}
func TestSourceQueueDropsBacklogAndBoundsMemory(t *testing.T) {
	c := &sourceCapture{wake: make(chan struct{}, 1)}
	for i := range 1000 {
		c.push(sourceChunk{first: time.Unix(int64(i), 0), pcm: []byte{byte(i)}})
	}
	if sourceQueueLen(c) != 3 {
		t.Fatal("unbounded queue")
	}
	for _, want := range []int64{997, 998, 999} {
		got, ok := c.pop()
		if !ok || got.first.Unix() != want {
			t.Fatal("did not drop oldest backlog")
		}
	}
	if _, ok := c.pop(); ok {
		t.Fatal("retained stale queue element")
	}
}

type sourceDiscontinuity struct{}

func (sourceDiscontinuity) Error() string                 { return "input discontinuity" }
func (sourceDiscontinuity) AudioInputDiscontinuity() bool { return true }
func TestSourceCaptureDiscontinuityPartialReadsAndFinalChunk(t *testing.T) {
	f := newFakeSourceInput()
	first := time.Now()
	f.reads <- sourceRead{samples: make([]float32, 80), first: first}
	f.reads <- sourceRead{samples: make([]float32, 80), first: first.Add(time.Second), err: sourceDiscontinuity{}}
	f.reads <- sourceRead{samples: make([]float32, 80), first: first.Add(time.Second + 10*time.Millisecond)}
	f.reads <- sourceRead{samples: make([]float32, 16), first: first.Add(time.Second + 20*time.Millisecond), err: io.EOF}
	c := newSourceCapture(context.Background(), f, audio.Format{SampleRate: 8000, Channels: 1}, make(chan struct{}, 1))
	<-c.done
	if !errors.Is(c.err, io.EOF) || f.closes.Load() != 1 {
		t.Fatal("capture failed to close/terminate", c.err)
	}
	a, ok := c.pop()
	if !ok || !a.first.Equal(first.Add(time.Second)) || len(a.pcm) != 320 {
		t.Fatal("old partial joined across discontinuity")
	}
	b, ok := c.pop()
	if !ok || len(b.pcm) != 32 || !b.first.Equal(first.Add(time.Second+20*time.Millisecond)) {
		t.Fatal("final short chunk lost")
	}
}
func TestSourceCaptureInvalidReadsAndOpenFailureAreBounded(t *testing.T) {
	for _, tc := range []struct {
		read    sourceRead
		openErr error
	}{
		{openErr: io.ErrUnexpectedEOF},
		{read: sourceRead{samples: make([]float32, 161), first: time.Now()}},
		{read: sourceRead{samples: make([]float32, 1), first: time.Now()}},
		{read: sourceRead{samples: make([]float32, 2)}},
	} {
		f := newFakeSourceInput()
		f.openErr = tc.openErr
		f.reads <- tc.read
		c := newSourceCapture(context.Background(), f, audio.Format{SampleRate: 8000, Channels: 2}, make(chan struct{}, 1))
		select {
		case <-c.done:
		case <-time.After(time.Second):
			c.cancel()
			t.Fatal("invalid input did not terminate")
		}
		if c.err == nil {
			t.Fatal("invalid backend result accepted")
		}
	}
}
func TestSourceRekeyPausesWritesAndPreservesOpenWithoutReopening(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		t.Run(suite, func(t *testing.T) {
			f := newFakeSourceInput()
			s, p := newSourceSchema(t, f, suite)
			sourceStart(t, s, p, f)
			c := s.source.capture
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
			c.push(sourceChunk{first: time.Now().Add(-time.Second), pcm: make([]byte, 320)})
			sourceWait(t, func() bool {
				c.mu.Lock()
				defer c.mu.Unlock()
				return monotime.Since(c.queue[0].queuedAt) > 100*time.Millisecond
			})
			if err = s.advanceSource(); err != nil || sourceQueueLen(c) != 1 {
				t.Fatal("source wrote during rekey", err)
			}
			schemaReceive(t, s, p, "server/activate", map[string]any{"activities": []string{}})
			expectSchemaPayload(t, p, "client/state", map[string]any{"available": true, "source": map[string]any{}})
			if err = s.advanceSource(); err != nil || sourceQueueLen(c) != 0 {
				t.Fatal("stale post-rekey backlog not dropped", err)
			}
			first := time.Now()
			c.push(sourceChunk{first: first, pcm: make([]byte, 320)})
			if err = s.advanceSource(); err != nil {
				t.Fatal(err)
			}
			frame := sourceAudio(t, p)
			want, _ := s.filter.CheckedServerTime(first.Sub(s.epoch).Microseconds() + 1)
			if int64(binary.BigEndian.Uint64(frame[1:9])) != want || f.opens.Load() != 1 || !s.source.open {
				t.Fatal("rekey reopened or retimestamped input")
			}
			if err = s.stopSource(); err != nil {
				t.Fatal(err)
			}
			expectSchemaPayload(t, p, "client-stream/end", map[string]any{})
			// Fresh connections have no authorization, even with the same config.
			next := newSession(s.client, s.ctx, func() {})
			if !reflect.DeepEqual(next.source, sourceState{}) {
				t.Fatal("reconnect retained source authorization")
			}
		})
	}
}
