package sendspin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrPlayerControlMode     = errors.New("sendspin: host-managed player control is not configured")
	ErrPlayerControlStale    = errors.New("sendspin: stale player control binding or generation")
	ErrPlayerControlOverload = errors.New("sendspin: player control queue is full")
)

// PlayerControlConfig explicitly disables software volume/mute gain. Empty
// configuration is a fixed/no-controls player until a host target is bound.
type PlayerControlConfig struct {
	// QueueCapacity bounds queued plus in-flight writes (default 16, max 256).
	// FIFO rejects overload and never coalesces accepted writes. A failed
	// executed setter withdraws all controls and cancels remaining queued writes.
	QueueCapacity int
	// RequestTimeout includes queue time (default 2s, maximum 1 minute).
	RequestTimeout time.Duration
}

func (c *Config) copyPlayerControl() error {
	if c.PlayerControl == nil {
		return nil
	}
	v := *c.PlayerControl
	if v.QueueCapacity == 0 {
		v.QueueCapacity = 16
	}
	if v.RequestTimeout == 0 {
		v.RequestTimeout = 2 * time.Second
	}
	if v.QueueCapacity < 1 || v.QueueCapacity > 256 || v.RequestTimeout <= 0 || v.RequestTimeout > time.Minute {
		return errors.New("sendspin: invalid host-managed player control limits")
	}
	c.PlayerControl = &v
	return nil
}

// PlayerControlCapabilities advertises independent absolute setters. Relative
// volume controls must NOT be adapted into SetVolume. Read-only state needs no setter.
type PlayerControlCapabilities struct {
	Volume bool
	Mute   bool
}

// PlayerControlReport is a complete, coherent observation, not a patch. Nil
// means unknown (including after loss of readback). Err is a nonterminal local
// diagnostic; withdraw capabilities explicitly if the backend cannot write.
// After automatic setter-failure withdrawal, a fresh report may restore them
// using the same valid target token (retain the returned token as usual).
// Reports never cause writes, never clear mute implicitly and are not persisted.
type PlayerControlReport struct {
	Capabilities PlayerControlCapabilities
	Volume       *int
	Muted        *bool
	Err          error
}

// PlayerControlTarget binds host-owned callbacks to one physical/logical target.
// The SDK invokes them serially, outside all protocol/audio loops and locks.
// Callbacks MUST honor context cancellation promptly; Go cannot forcibly stop
// arbitrary host code. A callback ignoring cancellation occupies the single
// worker, but never spawns replacement workers or blocks protocol/audio loops.
// Capture an immutable target, not a mutable global "current device". SetVolume
// is absolute 0..100 perceived loudness and MUST NOT unmute the target.
// Any error/panic after invocation, including an in-flight caller cancel or
// timeout, conservatively withdraws both controls until a fresh host report.
// Requests canceled/expired before invocation do not withdraw controls. Stale
// target/session/capability completions cannot withdraw a newer generation.
type PlayerControlTarget struct {
	SetVolume func(context.Context, int) error
	SetMuted  func(context.Context, bool) error
	Initial   PlayerControlReport
}

// PlayerControlBinding is an opaque generation token. Keep the token returned
// by ReportPlayerControl: capability and known/unknown changes replace it. Old
// tokens cannot update a replacement target or a later control generation.
// Automatic setter-failure withdrawal keeps this token valid for recovery reports.
type PlayerControlBinding struct{ generation *controlGeneration }
type controlGeneration struct{ _ byte } // Nonzero size keeps generation pointers distinct.

// PlayerControlRequest describes the latest accepted request for one control,
// independently of the observed state. Status: pending, succeeded, failed or
// canceled. Succeeded means only that the setter returned nil, NOT readback.
type PlayerControlRequest struct {
	ID     uint64
	Volume *int
	Muted  *bool
	Status string
	Error  string
}

type PlayerControlSnapshot struct {
	Capabilities  PlayerControlCapabilities
	VolumeRequest *PlayerControlRequest
	MuteRequest   *PlayerControlRequest
	// Error retains the latest host-report diagnostic or executed setter error,
	// independently of best-effort Events and the latest per-control request.
	// Only a fresh host report or target binding clears/replaces it.
	Error string
}

type controlWrite struct {
	ctx        context.Context
	cancel     context.CancelFunc
	stop       func() bool
	generation *controlGeneration
	epoch      uint64
	kind       string
	value      any
	id         uint64
	target     PlayerControlTarget
}

type playerControl struct {
	mu            sync.Mutex
	client        *Client
	cfg           PlayerControlConfig
	binding       PlayerControlBinding
	target        PlayerControlTarget
	report        PlayerControlReport
	state         PlayerControlSnapshot
	owner         *session
	epoch, nextID uint64
	queue         []*controlWrite
	inflight      *controlWrite
	running       bool
}

func newPlayerControl(c *Client, cfg PlayerControlConfig) *playerControl {
	return &playerControl{client: c, cfg: cfg}
}

func validateControlReport(target PlayerControlTarget, r PlayerControlReport) error {
	if r.Volume != nil && (*r.Volume < 0 || *r.Volume > 100) {
		return errors.New("sendspin: observed volume outside 0-100")
	}
	if r.Capabilities.Volume && target.SetVolume == nil || r.Capabilities.Mute && target.SetMuted == nil {
		return errors.New("sendspin: capability requires an explicit absolute volume or mute setter")
	}
	return nil
}

func cloneControlReport(r PlayerControlReport) PlayerControlReport {
	r.Volume, r.Muted = copyPointer(r.Volume), copyPointer(r.Muted)
	return r
}

// BindPlayerControl replaces the host target without writing hardware. It
// invalidates/cancels all old queued/in-flight writes, observations and tokens.
// An empty target is a fixed/no-controls backend with unknown state.
func (c *Client) BindPlayerControl(target PlayerControlTarget) (PlayerControlBinding, error) {
	p := c.playerControl
	if p == nil {
		return PlayerControlBinding{}, ErrPlayerControlMode
	}
	if err := validateControlReport(target, target.Initial); err != nil {
		return PlayerControlBinding{}, err
	}
	target.Initial = cloneControlReport(target.Initial)
	diagnostic := controlDiagnostic(target.Initial.Err)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidateLocked()
	p.binding = PlayerControlBinding{&controlGeneration{}}
	p.target = target
	p.setReportLocked(target.Initial, diagnostic)
	p.state.VolumeRequest, p.state.MuteRequest = nil, nil
	p.notifyLocked()
	return p.binding, nil
}

// ReportPlayerControl is thread-safe and never invokes a setter. The report is
// copied. A capability or known/unknown change cancels old writes and returns a
// fresh binding; value-only updates retain the token. Retain the returned token.
// Do not report values inferred merely from successful request completion.
func (c *Client) ReportPlayerControl(binding PlayerControlBinding, report PlayerControlReport) (PlayerControlBinding, error) {
	p := c.playerControl
	if p == nil {
		return PlayerControlBinding{}, ErrPlayerControlMode
	}
	// Error implementations are host code too; never invoke them under our lock.
	diagnostic := controlDiagnostic(report.Err)
	p.mu.Lock()
	defer p.mu.Unlock()
	if binding.generation == nil || binding != p.binding {
		return PlayerControlBinding{}, ErrPlayerControlStale
	}
	if err := validateControlReport(p.target, report); err != nil {
		return binding, err
	}
	if report.Capabilities != p.report.Capabilities || (report.Volume == nil) != (p.report.Volume == nil) || (report.Muted == nil) != (p.report.Muted == nil) {
		p.invalidateLocked()
		p.binding = PlayerControlBinding{&controlGeneration{}}
	}
	p.setReportLocked(cloneControlReport(report), diagnostic)
	p.notifyLocked()
	return p.binding, nil
}

func controlDiagnostic(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func (p *playerControl) setReportLocked(r PlayerControlReport, diagnostic string) {
	p.report = r
	p.state.Capabilities = r.Capabilities
	p.state.Error = diagnostic
}

func (p *playerControl) notifyLocked() {
	if p.owner == nil {
		return
	}
	select {
	case p.owner.controlUpdates <- struct{}{}:
	default:
	}
}

func (p *playerControl) latest(kind string) **PlayerControlRequest {
	if kind == "volume" {
		return &p.state.VolumeRequest
	}
	return &p.state.MuteRequest
}

func (p *playerControl) finishLocked(w *controlWrite, status, detail string) {
	r := *p.latest(w.kind)
	if r == nil || r.ID != w.id {
		return
	}
	r.Status, r.Error = status, detail
}

func (p *playerControl) invalidateLocked() {
	p.epoch++
	if p.inflight != nil {
		p.inflight.cancel()
		p.finishLocked(p.inflight, "canceled", ErrPlayerControlStale.Error())
	}
	for _, w := range p.queue {
		w.cancel()
		w.stop()
		p.finishLocked(w, "canceled", ErrPlayerControlStale.Error())
	}
	p.queue = nil
}

func (c *Client) setPlayerControlSession(s *session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !s.pairingCurrentLocked() || c.pairingRestarting || s.ctx.Err() != nil {
		return
	}
	if p := c.playerControl; p != nil {
		p.mu.Lock()
		if p.owner == s {
			p.mu.Unlock()
			return
		}
		p.invalidateLocked()
		p.owner = s
		p.notifyLocked()
		p.mu.Unlock()
	}
}

func (c *Client) endPlayerControlSession(s *session) {
	if c == nil {
		return
	} // Some protocol-only tests construct a bare session.
	if p := c.playerControl; p != nil {
		p.mu.Lock()
		if p.owner == s {
			p.invalidateLocked()
			p.owner = nil
			p.notifyLocked()
		}
		p.mu.Unlock()
	}
}

func (p *playerControl) enqueue(s *session, r request) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owner != s {
		return ErrInactiveRole
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.kind == "volume" && (!p.report.Capabilities.Volume || p.report.Volume == nil) || r.kind == "muted" && (!p.report.Capabilities.Mute || p.report.Muted == nil) {
		return ErrInactiveRole
	}
	n := len(p.queue)
	if p.inflight != nil {
		n++
	}
	if n >= p.cfg.QueueCapacity {
		return ErrPlayerControlOverload
	}
	ctx, cancel := context.WithTimeout(r.ctx, p.cfg.RequestTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	p.nextID++
	w := &controlWrite{ctx: ctx, cancel: cancel, stop: stop, generation: p.binding.generation, epoch: p.epoch, kind: r.kind, value: r.value, id: p.nextID, target: p.target}
	status := &PlayerControlRequest{ID: w.id, Status: "pending"}
	if r.kind == "volume" {
		v := r.value.(int)
		status.Volume = &v
	} else {
		v := r.value.(bool)
		status.Muted = &v
	}
	*p.latest(r.kind) = status
	p.queue = append(p.queue, w)
	if !p.running {
		p.running = true
		go p.work()
	}
	p.notifyLocked()
	return nil
}

func (p *playerControl) work() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.running = false
			p.mu.Unlock()
			return
		}
		w := p.queue[0]
		p.queue[0] = nil
		p.queue = p.queue[1:]
		p.inflight = w
		p.mu.Unlock()
		err := w.ctx.Err()
		invoked := err == nil
		if invoked {
			err = invokeControl(w)
		}
		// Preserve substantive backend errors/panics even if cancellation raced
		// with their return. A nil return after expiry still has uncertain effect.
		if err == nil && w.ctx.Err() != nil {
			err = w.ctx.Err()
		}
		w.cancel()
		w.stop()
		status, detail := "succeeded", controlDiagnostic(err)
		if err != nil {
			status = "failed"
			if detail == "" {
				detail = "sendspin: host player control write failed"
			}
			if errors.Is(err, context.Canceled) {
				status = "canceled"
			}
		}
		p.mu.Lock()
		current := w.generation == p.binding.generation && w.epoch == p.epoch
		// Detach before invalidating the queue: the failed request must not be
		// rewritten to canceled merely because its failure withdraws controls.
		p.inflight = nil
		if current {
			p.finishLocked(w, status, detail)
			// Session shutdown itself is not a backend failure, even if its
			// epoch-changing cleanup has not yet reached this worker.
			if invoked && err != nil && p.owner != nil && p.owner.ctx.Err() == nil {
				p.invalidateLocked()
				p.report.Capabilities = PlayerControlCapabilities{}
				p.state.Capabilities = PlayerControlCapabilities{}
				p.state.Error = detail
				// Keep the binding token valid: the host must be able to report
				// actual state/recovery without an asynchronous token handoff.
			}
			p.notifyLocked()
		}
		p.mu.Unlock()
		if current && err != nil {
			p.client.emit(Event{Kind: "player_control_error", Err: err})
		}
	}
}

func invokeControl(w *controlWrite) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("sendspin: host player control panic: %v", v)
		}
	}()
	if w.kind == "volume" {
		return w.target.SetVolume(w.ctx, w.value.(int))
	}
	return w.target.SetMuted(w.ctx, w.value.(bool))
}

func cloneControlRequest(r *PlayerControlRequest) *PlayerControlRequest {
	if r == nil {
		return nil
	}
	v := *r
	v.Volume = copyPointer(v.Volume)
	v.Muted = copyPointer(v.Muted)
	return &v
}

func (p *playerControl) apply(state *Snapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state.VolumeKnown, state.MutedKnown = p.report.Volume != nil, p.report.Muted != nil
	state.Volume, state.Muted = 0, false
	if p.report.Volume != nil {
		state.Volume = *p.report.Volume
	}
	if p.report.Muted != nil {
		state.Muted = *p.report.Muted
	}
	v := p.state
	v.VolumeRequest = cloneControlRequest(v.VolumeRequest)
	v.MuteRequest = cloneControlRequest(v.MuteRequest)
	state.PlayerControl = &v
}

func playerCommands(state Snapshot) []string {
	commands := make([]string, 0, 3)
	if state.PlayerControl == nil || state.PlayerControl.Capabilities.Volume && state.VolumeKnown {
		commands = append(commands, "volume")
	}
	if state.PlayerControl == nil || state.PlayerControl.Capabilities.Mute && state.MutedKnown {
		commands = append(commands, "mute")
	}
	return append(commands, "set_output_delay")
}

func (s *session) syncPlayerControl() {
	if s.client.playerControl != nil {
		s.client.playerControl.apply(&s.state)
	} else {
		s.state.VolumeKnown, s.state.MutedKnown = true, true
	}
}

func (s *session) engineState() {
	if s.engine == nil {
		return
	}
	// Only NewUnityGain selects the immutable gain bypass. Preserve logical
	// observations here, just as on the wire; never compensate them with 100%.
	s.engine.State(s.state.Volume, s.state.Muted, s.state.OutputDelayMS)
}
