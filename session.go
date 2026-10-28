package sendspin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/playback"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/timesync"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

type session struct {
	client                                         *Client
	ctx                                            context.Context
	cancel                                         context.CancelFunc
	done                                           chan struct{}
	requests                                       chan request
	displace                                       chan string
	channel                                        *wire.Channel
	filter                                         *timesync.Filter
	epoch                                          time.Time
	engine                                         *playback.Engine
	state                                          Snapshot
	snapshotMu                                     sync.RWMutex
	snapshot                                       []byte
	hello, activated, awaitingActivation, admitted bool
	// Admission and credential-protection fields are protected by Client.mu.
	rank                                int
	pairingAttempt                      bool
	retiring                            bool // Admission already decided a handoff; guarded by Client.mu.
	recordServerID                      string
	pendingPSK                          *Secret
	pairingDeadline                     time.Time
	pairingDiscard                      bool
	active                              map[string]Role
	streams                             map[string]bool
	decoder                             codec.AudioDecoder
	streamFormat                        codec.Format
	codecHeader                         string
	preferred                           *codec.Format
	willing                             bool
	timeOutstanding                     int64
	arrival                             int64
	burstLeft                           int
	bestOffset, bestUncertainty, bestAt int64
	nextBurst                           time.Time
	activationDeadline                  time.Time
	pendingDisplacement                 string
	fatal                               error
	pendingMetadata                     *Metadata
	pendingColors                       *Colors
	media                               mediaState
}

func newSession(c *Client, ctx context.Context, cancel context.CancelFunc) *session {
	started := monotime.Now()
	return &session{client: c, ctx: ctx, cancel: cancel, done: make(chan struct{}), requests: make(chan request), displace: make(chan string, 1), filter: timesync.New(), epoch: started, activationDeadline: started.Add(min(c.cfg.HandshakeTimeout, 30*time.Second)), active: map[string]Role{}, streams: map[string]bool{}, willing: true, burstLeft: 8}
}
func (s *session) now() int64         { return monotime.Since(s.epoch).Microseconds() + 1 }
func (s *session) has(role Role) bool { return slices.Contains(s.state.ActiveRoles, role) }
func (s *session) publish() {
	s.state.ActiveStreams = make([]string, 0, len(s.streams))
	for family, active := range s.streams {
		if active {
			s.state.ActiveStreams = append(s.state.ActiveStreams, family)
		}
	}
	slices.Sort(s.state.ActiveStreams)
	b, _ := json.Marshal(s.state)
	s.snapshotMu.Lock()
	s.snapshot = b
	s.snapshotMu.Unlock()
	s.client.emit(Event{Kind: "state_changed", ServerID: s.channel.ServerID})
}
func (c *Client) Snapshot() Snapshot {
	c.mu.Lock()
	s := c.active
	cred := c.credentials
	c.mu.Unlock()
	if s == nil {
		return Snapshot{Volume: cred.Volume, Muted: cred.Muted, OutputDelayMS: cred.OutputDelayMS}
	}
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	var state Snapshot
	_ = json.Unmarshal(s.snapshot, &state)
	return state
}

func (s *session) run() error {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	type received struct {
		frame   []byte
		err     error
		arrival int64
	}
	incoming := make(chan received)
	ack := make(chan struct{})
	readerDone := make(chan struct{})
	pingDone := make(chan struct{})
	pingError := make(chan error, 1)
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := s.channel.Conn.Ping(pingCtx)
				stop()
				if err != nil {
					select {
					case pingError <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-pingDone }()
	go func() {
		defer close(readerDone)
		for {
			frame, at, err := s.channel.ReadTimed(ctx)
			arrival := at.Sub(s.epoch).Microseconds() + 1
			select {
			case <-ctx.Done():
				return
			case incoming <- received{frame: frame, err: err, arrival: arrival}:
			}
			if err != nil {
				return
			}
			// Do not read using the previous receive key while the actor is
			// processing an in-band re-handshake and replacing cipher states.
			select {
			case <-ctx.Done():
				return
			case <-ack:
			}
		}
	}()
	defer func() { cancel(); <-readerDone }()
	var engineDone chan error
	defer func() {
		if s.engine != nil {
			cancel()
			<-engineDone // Cleanup: preserve the actor's primary result.
		}
	}()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	// RC1's provisional-connection budget includes the initial handshake;
	// starting a second full timeout here could keep a connection for 60s.
	activationTimeout := time.NewTimer(monotime.Until(s.activationDeadline))
	defer activationTimeout.Stop()
	for {
		select {
		case <-ctx.Done():
			s.shutdownGoodbye()
			return ctx.Err()
		case reason := <-s.displace:
			if err := s.displaced(reason); err != nil {
				return err
			}
		case err := <-engineDone:
			engineDone = nil
			s.engine = nil
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.client.logger().With("error_class", logErrorClass(err)).Warn("Sendspin audio worker stopped")
			return fmt.Errorf("sendspin: audio output: %w", err)
		case err := <-pingError:
			return err
		case <-activationTimeout.C:
			if !s.activated {
				return fmt.Errorf("%w: activation timeout", protocol.ErrProtocol)
			}
		case got := <-incoming:
			if got.err != nil {
				return got.err
			}
			s.arrival = got.arrival
			if err := s.dispatch(got.frame); err != nil {
				return err
			}
			if s.admitted && s.engine == nil && slices.Contains(s.client.cfg.Roles, Player) {
				s.engine = playback.New(s.client.cfg.Output, s.client.cfg.BufferCapacity, s.local)
				s.engine.State(s.state.Volume, s.state.Muted, s.state.OutputDelayMS)
				engineDone = make(chan error, 1)
				engine, done := s.engine, engineDone
				go func() { done <- engine.Run(ctx) }()
			}
			select {
			case ack <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case r := <-s.requests:
			if err := r.ctx.Err(); err != nil {
				r.done <- err
				continue
			}
			r.done <- s.request(r)
			if s.fatal != nil {
				return s.fatal
			}
		case <-tick.C:
			if s.awaitingActivation && monotime.Now().After(s.activationDeadline) {
				return fmt.Errorf("%w: re-handshake activation timeout", protocol.ErrProtocol)
			}
			if !s.activated || s.awaitingActivation {
				continue
			}
			if _, err := s.checkPairingTimeout(monotime.Now()); err != nil {
				return err
			}
			if s.timeOutstanding != 0 && s.now()-s.timeOutstanding > 10_000_000 {
				return errors.New("sendspin: clock synchronization timeout")
			}
			if s.timeOutstanding == 0 && (s.burstLeft > 0 || monotime.Now().After(s.nextBurst)) {
				if s.burstLeft == 0 {
					s.burstLeft = 8
					s.bestUncertainty = 0
				}
				s.timeOutstanding = s.now()
				if err := s.send("client/time", map[string]int64{"client_transmitted": s.timeOutstanding}); err != nil {
					return err
				}
			}
			if err := s.scheduled(); err != nil {
				return err
			}
		}
	}
}

func (s *session) send(kind string, payload any) error {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	err := s.channel.JSON(ctx, kind, payload)
	if err != nil {
		s.fatal = err
	}
	return err
}
func (s *session) goodbye(reason string) {
	if !validGoodbyeReason(reason) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = s.channel.JSON(ctx, "client/goodbye", map[string]string{"reason": reason})
}

func (s *session) shutdownGoodbye() {
	// A shutdown notification is recommended, but the re-handshake's ban on
	// application writes lasts until the next activation. Close silently there.
	if s.hello && !s.awaitingActivation {
		s.goodbye("shutdown")
	}
}
func (s *session) failGoodbye(reason string) error {
	if !validGoodbyeReason(reason) {
		return protocol.ErrProtocol
	}
	s.goodbye(reason)
	return &GoodbyeError{reason}
}

func validGoodbyeReason(reason string) bool {
	switch reason {
	case "another_server", "shutdown", "restart", "user_request", "unauthorized", "pairing_required", "concurrent_attempt", "unpaired":
		return true
	default:
		return false
	}
}

func (s *session) displaced(reason string) error {
	if s.awaitingActivation {
		// Re-handshake permits no application writes before the activation
		// under the new keys. Retain the admission decision, but defer goodbye.
		s.pendingDisplacement = reason
		return nil
	}
	return s.failGoodbye(reason)
}

func (s *session) dispatch(frame []byte) error {
	if len(frame) == 0 {
		return protocol.ErrProtocol
	}
	if frame[0] != 0 {
		if !s.activated || s.awaitingActivation {
			return protocol.ErrProtocol
		}
		return s.binary(frame)
	}
	m, err := protocol.Parse(frame[1:])
	if err != nil {
		return err
	}
	if !s.hello {
		if m.Type != "server/hello" {
			return protocol.ErrProtocol
		}
		hello, err := decodeServerHello(m.Payload)
		if err != nil {
			return err
		}
		s.state.ServerID, s.state.ServerName = s.channel.ServerID, hello.Name
		s.state.ServerLanguages = hello.Languages
		s.client.mu.Lock()
		cred := s.client.credentials
		s.client.mu.Unlock()
		s.state.Volume, s.state.Muted, s.state.OutputDelayMS = cred.Volume, cred.Muted, cred.OutputDelayMS
		s.hello = true
		return s.sendHello()
	}
	if (!s.activated || s.awaitingActivation) && m.Type != "server/activate" {
		return protocol.ErrProtocol
	}
	// Timer selection must not decide whether a late finalize can persist.
	// Only pairing messages trigger this check: malformed unrelated messages
	// still fail silently, activation supersedes the old attempt, and receiving
	// Noise message 1 already forbids application writes.
	if strings.HasPrefix(m.Type, "server/pair-") || m.Type == "pair/abort" {
		if _, err := s.checkPairingTimeout(monotime.Now()); err != nil {
			return err
		}
	}
	if s.pairingDiscard && strings.HasPrefix(m.Type, "server/pair-") {
		return nil
	}
	switch m.Type {
	case "server/hello", "server/init", "client/hello", "client/init", "server/error", "client/time", "client/state", "client/command", "client/leave", "client/goodbye", "client/pair-init", "client/pair-finalize", "client/pair-pending", "client/pair-auth", "client/pair-confirm", "client/pair-retry":
		return protocol.ErrProtocol
	case "server/pair-init", "server/pair-auth", "server/pair-confirm":
		// These messages belong to code-based methods, never the offered PSK
		// method. The in-flight discard exception was handled above.
		return protocol.ErrProtocol
	case "server/activate":
		return s.activate(m.Payload)
	case "noise/handshake":
		ctx, cancel := context.WithTimeout(s.ctx, s.client.cfg.HandshakeTimeout)
		defer cancel()
		if err = s.channel.Rehandshake(ctx, m.Payload); err != nil {
			return err
		}
		s.commitChannel(s.channel)
		s.restartTimeExchange()
		s.media.pairingIndex = 0
		s.awaitingActivation = true
		s.activationDeadline = monotime.Now().Add(s.client.cfg.HandshakeTimeout)
		return nil
	case "server/time":
		return s.timeSample(m.Payload)
	case "server/state":
		return s.serverState(m.Payload)
	case "server/command":
		return s.serverCommand(m.Payload)
	case "group/update":
		var g Group
		if err = protocol.Decode(m.Payload, &g, "group_id", "group_name", "playback_state"); err != nil {
			return err
		}
		if g.PlaybackState != "playing" && g.PlaybackState != "stopped" {
			return protocol.ErrProtocol
		}
		s.state.Group = g
		s.publish()
		return nil
	case "stream/start":
		return s.startStream(m.Payload)
	case "stream/clear", "stream/end":
		return s.stopStream(m.Type, m.Payload)
	case "server/pair-finalize":
		return s.finalizePairing(m.Payload)
	case "pair/abort":
		if s.pairingDiscard || s.pendingPSK == nil {
			return nil
		}
		var abort struct {
			Reason string `json:"reason"`
		}
		if err = protocol.Decode(m.Payload, &abort, "reason"); err != nil {
			return err
		}
		if !slices.Contains([]string{"user_cancelled", "pairing_code_mismatch"}, abort.Reason) {
			return protocol.ErrProtocol
		}
		s.endPairing(true)
		return nil
	case "server/unpair":
		if s.channel.Category != "lt" {
			return nil
		}
		if err = s.client.update(s.ctx, func(c *Credentials) error {
			c.Records = slices.DeleteFunc(c.Records, func(r PairingRecord) bool { return r.ServerID == s.channel.ServerID })
			return nil
		}); err != nil {
			return err
		}
		return s.failGoodbye("unpaired")
	default:
		// Unknown well-formed envelopes are forward-compatible after activation.
		return nil
	}
}

func (s *session) sendHello() error {
	cfg := s.client.cfg
	p := map[string]any{"name": cfg.Name, "supported_roles": cfg.Roles, "supported_pair_methods": map[string]any{"pairing_psk": map[string]any{}}, "unpaired_access": map[string]bool{"enabled": cfg.AllowUnpaired}}
	if cfg.DeviceInfo != nil {
		p["device_info"] = cfg.DeviceInfo
	}
	if slices.Contains(cfg.Roles, Player) {
		p["player@v1_support"] = map[string]any{"supported_formats": cfg.Formats, "buffer_capacity": cfg.BufferCapacity}
	}
	if slices.Contains(cfg.Roles, VisualizerRole) {
		p["visualizer@v1_support"] = map[string]int{"buffer_capacity": cfg.VisualizerCapacity}
	}
	return s.send("client/hello", p)
}

func (s *session) activate(data []byte) error {
	var a struct {
		Activities []string        `json:"activities"`
		Roles      *[]Role         `json:"active_roles"`
		Pairing    json.RawMessage `json:"pairing"`
	}
	if err := protocol.Decode(data, &a, "activities"); err != nil {
		return err
	}
	seen := map[string]bool{}
	rank := 0
	for _, activity := range a.Activities {
		if seen[activity] {
			return protocol.ErrProtocol
		}
		seen[activity] = true
		switch activity {
		case "playback":
			rank = 2
		case "pairing":
			rank = max(rank, 1)
		default:
			return protocol.ErrProtocol
		}
	}
	// The field's own rule explicitly ignores it without a pairing activity,
	// including its shape. Defer validation rather than weakening Decode's
	// rejection of null/incorrect types for other known fields.
	pairingSupported := true
	if seen["pairing"] {
		var pairing struct {
			Method string  `json:"method"`
			Format *string `json:"format"`
		}
		if err := protocol.Decode(a.Pairing, &pairing, "method"); err != nil {
			return err
		}
		if pairing.Method == "dynamic_pairing_code" && pairing.Format == nil {
			return protocol.ErrProtocol
		}
		pairingSupported = pairing.Method == "pairing_psk" && s.channel.Category == "pr" && pairing.Format == nil
	}
	roles := s.state.ActiveRoles
	if a.Roles != nil {
		roles = *a.Roles
	} else if !s.activated {
		roles = []Role{}
	}
	paired := s.channel.Category == "lt"
	allowed := !paired || !seen["pairing"]
	playbackCapable := allowed && (paired || s.client.cfg.AllowUnpaired)
	if a.Roles == nil && !playbackCapable {
		roles = []Role{}
	}
	active := map[string]Role{}
	for _, role := range roles {
		family, _, ok := strings.Cut(string(role), "@")
		if !ok || !slices.Contains(s.client.cfg.Roles, role) || active[family] != "" {
			return protocol.ErrProtocol
		}
		active[family] = role
	}
	if seen["pairing"] {
		if s.media.pairingIndex == ^uint32(0) {
			return protocol.ErrProtocol // A new handshake is needed before the counter wraps.
		}
		s.media.pairingIndex++
	}
	refusal := s.activationRefusal(seen, roles, pairingSupported)
	if refusal == "unauthorized" || refusal == "pairing_required" {
		return s.failGoodbye(refusal)
	}
	if refusal == "method_not_supported" {
		if err := s.rejectPairingActivation(playbackCapable); err != nil {
			return err
		}
		if err := s.send("pair/abort", map[string]string{"reason": "method_not_supported"}); err != nil {
			return err
		}
		// Receipt ended the re-handshake barrier. A previously decided server
		// handoff must complete now, even if this activation was not accepted;
		// waiting for another activation can pin admission behind this socket.
		if s.pendingDisplacement != "" {
			return s.failGoodbye(s.pendingDisplacement)
		}
		return nil
	}
	if !s.admitted {
		if err := s.client.admit(s, rank, seen["pairing"]); err != nil {
			return err
		}
		s.admitted = true
	}
	for family := range s.active {
		if active[family] == s.active[family] {
			continue
		}
		if s.streams[family] {
			return protocol.ErrProtocol
		}
		s.clearRole(family)
	}
	if seen["playback"] {
		if err := s.client.update(s.ctx, func(c *Credentials) error { c.LastPlaybackServer = s.channel.ServerID; return nil }); err != nil {
			return err
		}
	}
	s.client.mu.Lock()
	s.rank = rank
	s.client.mu.Unlock()
	s.active, s.state.ActiveRoles = active, slices.Clone(roles)
	s.activated, s.awaitingActivation = true, false
	s.publish()
	s.client.logger().With("active_roles", len(roles)).With("playback", seen["playback"]).With("pairing", seen["pairing"]).Debug("Sendspin activation accepted")
	if s.pendingDisplacement != "" {
		if seen["pairing"] {
			if err := s.send("pair/abort", map[string]string{"reason": "concurrent_attempt"}); err != nil {
				return err
			}
			return &GoodbyeError{"concurrent_attempt"}
		}
		return s.failGoodbye(s.pendingDisplacement)
	}
	if seen["pairing"] {
		s.pairingDiscard = false
		var psk Secret
		if _, err := rand.Read(psk[:]); err != nil {
			return err
		}
		s.pendingPSK = &psk
		s.pairingDeadline = monotime.Now().Add(2 * time.Minute)
		if !s.beginPairingAttempt() {
			s.endPairing(true)
			if err := s.send("pair/abort", map[string]string{"reason": "concurrent_attempt"}); err != nil {
				return err
			}
			return &GoodbyeError{"concurrent_attempt"}
		}
		if err := s.send("client/pair-init", map[string]uint32{"pairing_index": s.media.pairingIndex}); err != nil {
			return err
		}
		expired, err := s.checkPairingTimeout(monotime.Now())
		if err != nil {
			return err
		}
		if !expired {
			if err = s.send("client/pair-finalize", map[string]string{"long_term_psk": base64.RawURLEncoding.EncodeToString(psk[:])}); err != nil {
				return err
			}
			if _, err = s.checkPairingTimeout(monotime.Now()); err != nil {
				return err
			}
		}
	} else {
		// A received activation is the same-direction barrier ending the
		// discard window. Pairing messages following it cannot be leftovers.
		s.endPairing(false)
	}
	if len(roles) > 0 {
		return s.sendState()
	}
	return nil
}

func (s *session) setPairingAttempt(active bool) {
	s.client.mu.Lock()
	s.pairingAttempt = active
	s.client.mu.Unlock()
}

func (s *session) beginPairingAttempt() bool {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if s.retiring {
		return false
	}
	// Linearize protection before transmission: the peer may receive pair-init
	// before Write (or an observer after Write) returns to this actor.
	s.pairingAttempt = true
	return true
}

func (c *Client) admit(s *session, rank int, incomingPairing bool) error {
	c.admission.Lock()
	defer c.admission.Unlock()
	c.mu.Lock()
	if err := s.ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	old := c.active
	reject := false
	if old != nil && old != s {
		reject = old.pairingAttempt || rank < old.rank
		if old.rank == 0 && rank == 0 {
			reject = s.channel.ServerID != c.credentials.LastPlaybackServer || old.channel.ServerID == c.credentials.LastPlaybackServer
		}
	}
	if !reject && old != nil && old != s {
		old.retiring = true
	}
	c.mu.Unlock()
	if reject {
		if incomingPairing {
			_ = s.send("pair/abort", map[string]string{"reason": "concurrent_attempt"})
			return &GoodbyeError{"concurrent_attempt"}
		}
		return s.failGoodbye("concurrent_attempt")
	}
	if old != nil && old != s {
		select {
		case old.displace <- "another_server":
		default:
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-old.done:
		}
	}
	c.mu.Lock()
	if err := s.ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	s.rank = rank // Publish the declared priority atomically with its holder.
	c.active = s
	c.mu.Unlock()
	return nil
}

func (s *session) finalizePairing(data []byte) error {
	if expired, err := s.checkPairingTimeout(monotime.Now()); expired || err != nil {
		return err
	}
	if !protocol.Object(data) || s.pendingPSK == nil {
		return protocol.ErrProtocol
	}
	psk := *s.pendingPSK
	storeCtx := s.ctx
	if !s.pairingDeadline.IsZero() {
		var cancel context.CancelFunc
		storeCtx, cancel = context.WithDeadline(s.ctx, s.pairingDeadline)
		defer cancel()
	}
	err := s.client.update(storeCtx, func(c *Credentials) error {
		// Waiting for another credential transaction must not extend an attempt.
		if !s.pairingDeadline.IsZero() && !monotime.Now().Before(s.pairingDeadline) {
			return errPairingDeadline
		}
		c.Records = slices.DeleteFunc(c.Records, func(r PairingRecord) bool { return r.ServerID == s.channel.ServerID })
		if len(c.Records) >= s.client.cfg.RecordCapacity {
			for i, r := range c.Records {
				inUse := false
				for conn := range s.client.sessions {
					if conn.recordServerID == r.ServerID {
						inUse = true
						break
					}
				}
				if !inUse {
					c.Records = slices.Delete(c.Records, i, i+1)
					break
				}
			}
			if len(c.Records) >= s.client.cfg.RecordCapacity {
				return errors.New("sendspin: no evictable pairing record")
			}
		}
		c.Records = append(c.Records, PairingRecord{ServerID: s.channel.ServerID, PSK: psk})
		return nil
	})
	if errors.Is(err, errPairingDeadline) || (errors.Is(err, context.DeadlineExceeded) && !s.pairingDeadline.IsZero() && !monotime.Now().Before(s.pairingDeadline)) {
		_, err = s.checkPairingTimeout(monotime.Now())
		return err
	}
	if err == nil {
		s.endPairing(false)
		s.client.emit(Event{Kind: "paired", ServerID: s.channel.ServerID})
		s.client.logger().Info("Sendspin pairing record persisted")
	}
	return err
}

func (s *session) sendState() error {
	ready := s.willing && (!s.has(Player) || s.filter.Ready())
	s.state.Available = ready
	p := map[string]any{"available": ready}
	if s.has(Player) {
		player := map[string]any{"volume": s.state.Volume, "muted": s.state.Muted, "output_delay_ms": s.state.OutputDelayMS, "required_lead_time_ms": s.client.cfg.RequiredLeadTime.Milliseconds(), "min_buffer_ms": s.client.cfg.MinBuffer.Milliseconds(), "supported_commands": []string{"volume", "mute", "set_output_delay"}}
		if s.preferred != nil {
			player["format"] = s.preferred
		}
		p["player"] = player
	}
	if s.has(ArtworkRole) {
		p["artwork"] = map[string]any{"channels": s.client.cfg.Artwork}
	}
	if s.has(VisualizerRole) {
		p["visualizer"] = s.client.cfg.Visualizer
	}
	if s.engine != nil {
		s.engine.State(s.state.Volume, s.state.Muted, s.state.OutputDelayMS)
	}
	s.publish()
	return s.send("client/state", p)
}

func (s *session) timeSample(data []byte) error {
	// Capture at the decrypted/reassembled application boundary, not after
	// actor scheduling, storage work, or another role's handler added delay.
	arrival := s.arrival
	if arrival == 0 {
		arrival = s.now()
	}
	var t struct {
		Client   int64 `json:"client_transmitted"`
		Received int64 `json:"server_received"`
		Sent     int64 `json:"server_transmitted"`
	}
	if err := protocol.Decode(data, &t, "client_transmitted", "server_received", "server_transmitted"); err != nil {
		return err
	}
	if t.Client != s.timeOutstanding || t.Client == 0 {
		// A server may discard old-key application requests during re-handshake
		// or send an already queued response after the new activation. Such a
		// response cannot update the filter or complete the current exchange.
		return nil
	}
	s.timeOutstanding = 0
	s.burstLeft--
	offset, uncertainty, ok := timesync.Sample(t.Client, t.Received, t.Sent, arrival)
	if ok && (s.bestUncertainty == 0 || uncertainty < s.bestUncertainty) {
		s.bestOffset, s.bestUncertainty, s.bestAt = offset, uncertainty, arrival
	}
	// Feed the first three samples promptly at startup, then use the minimum
	// uncertainty of each eight-exchange burst as recommended by time-filter.
	if ok && !s.filter.Ready() {
		s.filter.Update(offset, uncertainty, arrival)
	}
	if s.burstLeft <= 0 {
		if s.bestUncertainty > 0 {
			s.filter.Update(s.bestOffset, s.bestUncertainty, s.bestAt)
		}
		s.nextBurst = monotime.Now()
		if s.filter.Ready() {
			s.nextBurst = s.nextBurst.Add(10 * time.Second)
		}
		// Coarse clocks can quantize a fast loopback round trip to zero. Such
		// samples remain invalid, but must not put an unconverged startup into
		// the steady-state ten-second idle period. The actor's 5-ms ticker still
		// bounds the request rate; no timestamp/uncertainty is manufactured.
	}
	ready := s.willing && (!s.has(Player) || s.filter.Ready())
	if ready != s.state.Available && len(s.active) > 0 {
		return s.sendState()
	}
	return nil
}

func (s *session) restartTimeExchange() {
	// The filter itself persists across a re-handshake. An outstanding request
	// does not: the server is explicitly allowed not to answer it.
	s.timeOutstanding = 0
	s.burstLeft = 8
	s.bestOffset, s.bestUncertainty, s.bestAt = 0, 0, 0
	s.nextBurst = time.Time{}
}

func (s *session) binary(frame []byte) error {
	if frame[0] == 4 {
		if !s.has(Player) || !s.streams["player"] || s.decoder == nil || len(frame) < 14 {
			return protocol.ErrProtocol
		}
		timestamp := int64(binary.BigEndian.Uint64(frame[1:9]))
		if hook := s.client.cfg.Encoded; hook != nil {
			hook(diagnostic.AudioChunk{Format: s.streamFormat, Timestamp: timestamp, SendAheadUS: binary.BigEndian.Uint32(frame[9:13]), Payload: frame[13:]})
		}
		samples, err := s.decoder.Decode(frame[13:])
		if err != nil {
			return fmt.Errorf("%w: %v", protocol.ErrProtocol, err)
		}
		if !s.state.Available {
			return nil
		}
		if hook := s.client.cfg.Decoded; hook != nil {
			hook(s.streamFormat, samples)
		}
		if s.engine == nil {
			return protocol.ErrProtocol
		}
		return s.engine.Enqueue(playback.Chunk{Format: audio.Format{SampleRate: s.streamFormat.SampleRate, Channels: s.streamFormat.Channels}, Timestamp: timestamp, Samples: samples, WireBytes: len(frame)})
	}
	return s.mediaBinary(frame)
}
