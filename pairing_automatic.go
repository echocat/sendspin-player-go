package sendspin

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/echocat/sendspin-player-go/internal/monotime"
)

const pairingCooldown = 30 * time.Second

var ErrPairingRestarting = errors.New("sendspin: pairing restart or session revocation in progress")

func pairingNow() time.Time { return monotime.Now() }

// PairingState contains no secrets. Paired means at least one durable record,
// not an online server. Ready means a new automatic PIN attempt can start;
// Active means an attempt already owns the device. State is sampled, not a lease.
type PairingState struct {
	Automatic bool
	Paired    bool
	Ready     bool
	Active    bool
	// RequiresOperator reports the persistent 20-round lockout. Release it with
	// an intentional OpenPairingWindow action or confirmed RestartPairing.
	RequiresOperator bool
	// Paused reports cancellation for this process; OpenPairingWindow resumes it.
	Paused bool
	// Restarting blocks new sessions and PINs until revoked actors have exited.
	Restarting bool
	// StorageBlocked requires fixing storage and explicitly releasing the pause.
	StorageBlocked      bool
	FailedConfirmations uint8
	Rounds              uint32
	// CooldownRemaining is monotonic, not computed from a persisted wall clock.
	CooldownRemaining time.Duration
	// Revision is a process-local change counter, not a durable identifier or
	// authorization capability. It changes on rounds, attempts and invalidation.
	Revision uint64
}

// PairingPINDisplay is explicitly requested secret UI data, never a Snapshot.
// PIN formatting/JSON is redacted; Value/Grouped deliberately reveal it.
// ValidUntil is an upper bound, not a lease: cancellation, confirmation,
// disconnect or reset can invalidate it earlier. Do not retain UI copies.
type PairingPINDisplay struct {
	PIN        PairingPIN
	ValidUntil time.Time
	Revision   uint64
}

func (c *Client) automaticPairing() bool {
	return c.cfg.CodePairing != nil && c.cfg.CodePairing.Automatic
}

func (c *Client) pairingChangedLocked() {
	c.pairingRevision++
	c.emit(Event{Kind: "pairing_changed"})
}

// No policy worker is needed: session ticks and public reads reconcile deadlines.
// A persisted cooldown is cleared by the next admitted round's transaction, not
// by a wall-clock timestamp. Reboot always waits a fresh full 30 seconds.
func (c *Client) refreshPairingLocked() {
	changed := false
	if c.pairingRestarting && len(c.sessions) == 0 {
		c.pairingRestarting = false
		changed = true
	}
	if !c.pairingCooldownUntil.IsZero() && !pairingNow().Before(c.pairingCooldownUntil) {
		c.pairingCooldownUntil = time.Time{}
		changed = true
	}
	if !c.pairingPIN.ValidUntil.IsZero() && !pairingNow().Before(c.pairingPIN.ValidUntil) {
		c.pairingPIN = PairingPINDisplay{}
		changed = true
	}
	if changed {
		c.pairingChangedLocked()
	}
}

func (c *Client) pairingStateLocked() PairingState {
	c.refreshPairingLocked()
	p := PairingState{
		Automatic: c.automaticPairing(), Paired: len(c.credentials.Records) > 0,
		RequiresOperator: c.credentials.DynamicPairingRounds >= 20,
		Paused:           c.pairingPaused, Restarting: c.pairingRestarting,
		StorageBlocked: c.pairingBlocked, FailedConfirmations: c.credentials.PairingFailures,
		Rounds: c.credentials.DynamicPairingRounds, Revision: c.pairingRevision,
		CooldownRemaining: max(0, monotime.Until(c.pairingCooldownUntil)),
	}
	for s := range c.sessions {
		p.Active = p.Active || (s.pairingAttempt && !s.revoked)
	}
	p.Ready = p.Automatic && !p.Paired && !p.Active && !p.RequiresOperator && !p.Paused && !p.Restarting && !p.StorageBlocked && p.CooldownRemaining == 0
	return p
}

// PairingState returns non-secret readiness and protection state. Events sends
// best-effort pairing_changed notifications; re-read this state after an event
// and when refreshing a UI, since the bounded event channel may drop events.
// Ready is false while paired, active, cooling down, locked out, paused, blocked
// by storage or cleaning up a restart. Offline server records remain Paired.
// See [CodePairingConfig] for counting, persistence and clock semantics.
func (c *Client) PairingState() PairingState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pairingStateLocked()
}

// CurrentPairingPIN explicitly retrieves the automatic mode's active six-digit
// PIN for local display or an authenticated administration UI. The application
// must authenticate/authorize remote callers. No PIN exists before server init,
// or after confirmation, expiry, cancellation, disconnect, reset or durable pairing.
//
// Only PIN.Value and PIN.Grouped deliberately reveal plaintext; formatting and
// JSON redact it. Never forward extracted strings to logs, telemetry or generic
// snapshots. Hide the UI when unavailable and refresh after pairing_changed
// events and periodically while visible: events can be dropped and ValidUntil
// is only an upper bound. The SDK cannot erase copies retained by the host.
// The host must also secure remote administration transport and, where relevant,
// protect against CSRF. [Credentials] serialization and [Client.PairingToken]
// are separate secret persistence/provisioning interfaces, not UI snapshots.
func (c *Client) CurrentPairingPIN() (PairingPINDisplay, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pairingStateLocked()
	if !p.Automatic || p.Paired || p.Paused || p.Restarting || p.StorageBlocked || c.pairingPIN.PIN.value == "" || c.pairingPINOwner == nil || c.pairingPINOwner.ctx.Err() != nil {
		return PairingPINDisplay{}, false
	}
	pin := c.pairingPIN
	pin.Revision = c.pairingRevision
	return pin, true
}

func (s *session) pairingCurrentLocked() bool {
	return !s.revoked && s.pairingGeneration == s.client.pairingGeneration
}

func (s *session) checkPairingGeneration() error {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if !s.pairingCurrentLocked() || s.client.pairingRestarting {
		return ErrPairingRestarting
	}
	return s.ctx.Err()
}

// Session-owned writes must check revocation INSIDE the credential transaction.
// Checking only context before waiting for Client.mu permits a late finalize.
func (s *session) update(ctx context.Context, f func(*Credentials) error) error {
	return s.client.update(ctx, func(c *Credentials) error {
		if !s.pairingCurrentLocked() || s.client.pairingRestarting {
			return ErrPairingRestarting
		}
		return f(c)
	})
}

// PairingRestartResult distinguishes cancellation before and after the durable
// commit. Committed=true is irreversible even when the returned error is non-nil.
type PairingRestartResult struct{ Committed bool }

// RestartPairing is an explicit ADMINISTRATOR operation for Automatic mode.
// The host MUST authenticate, authorize and obtain confirmation from the operator.
// It preserves identity, audio and unrelated settings, replaces the bootstrap
// PSK/token, removes every server record and revokes all old session generations.
// Without CodePairingConfig.Automatic it returns ErrPairingDisabled. It works
// while paired and does not require an active connection.
//
// The reset also removes the old static PIN and last-playback server hint. The
// private key/client ID, volume, mute, output delay and host audio target bindings
// remain intact. The explicit administrator action releases the 20-round lockout
// but retains short failure counts/cooldown. An unresolved authentication becomes
// a full cooldown. A fresh PIN exists only after a new server-started attempt,
// not immediately upon reset; see [CodePairingConfig] for protocol constraints.
//
// Commit point: successful atomic Store.Save under the credential lock. Before
// it, failure has no effects. Afterwards revocation is unconditional, even when
// ctx is canceled; Committed is true. Cancellation can stop waiting for cleanup,
// but cannot roll back credentials. New sessions/PINs remain blocked until old
// actors have exited. Retrying would perform ANOTHER reset, not finish the first.
// Store must obey the atomic Save contract (nil means committed, error means no
// replacement). As with other SDK calls a context-ignoring Store cannot be preempted.
//
// After commit, transport closure and revocation include provisional handshakes.
// Credential writes and finalization check the session generation inside the
// same transaction lock: finalize either commits first and is erased by reset,
// or reset commits first and the old finalize is rejected. Expiry and ordinary
// finalization execute serially in the session actor. Aborted in-flight messages
// are discarded until the next activation barrier. Old tokens/PSKs cannot
// authenticate: initial unknown credentials follow unauthenticated Sentinel
// fallback; old re-handshake credentials are rejected.
//
// A post-commit context error stops waiting, not revocation/transport closure.
// Old actors finish cleanup themselves; state reads or the next connection
// reconcile completion without another worker. An uncooperative callback/backend
// keeps new readiness closed. The SDK cannot undo external hardware effects or
// revoke application-held secret copies. A new process loads only the committed
// snapshot. Success means Committed=true and err=nil, with all old actors exited;
// retained cooldown protection may still postpone Ready.
func (c *Client) RestartPairing(ctx context.Context) (PairingRestartResult, error) {
	var result PairingRestartResult
	if !c.automaticPairing() {
		return result, ErrPairingDisabled
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	var psk Secret
	if _, err := rand.Read(psk[:]); err != nil {
		return result, err
	}
	c.mu.Lock()
	c.refreshPairingLocked()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return result, err
	}
	if c.pairingRestarting {
		c.mu.Unlock()
		return result, ErrPairingRestarting
	}
	next := cloneCredentials(c.credentials)
	for psk == next.PairingPSK || psk == (Secret{}) || psk == sentinelPSK {
		if _, err := rand.Read(psk[:]); err != nil {
			c.mu.Unlock()
			return result, err
		}
	}
	next.Records = nil
	next.LastPlaybackServer = ""
	next.PairingPSK = psk
	next.StaticPairingPIN = nil
	next.DynamicPairingRounds = 0 // This confirmed administrator action releases lockout.
	// Reset is not a way to evade short-term rate protection.
	if next.PairingAuthPending {
		next.PairingCooldown = true
		next.PairingAuthPending = false
	}
	if err := c.cfg.Store.Save(ctx, cloneCredentials(next)); err != nil {
		c.mu.Unlock()
		return result, err
	}
	result.Committed = true
	c.credentials = next
	c.pairingGeneration++
	c.pairingRestarting = true
	c.pairingPaused, c.pairingBlocked = false, false
	c.pairWindow = pairingWindow{}
	c.pairingPIN = PairingPINDisplay{}
	if next.PairingCooldown {
		c.pairingCooldownUntil = pairingNow().Add(pairingCooldown)
	}
	sessions := make([]*session, 0, len(c.sessions))
	for s := range c.sessions {
		s.revoked, s.retiring = true, true
		s.cancel()
		sessions = append(sessions, s)
	}
	c.active = nil
	c.pairingChangedLocked()
	c.mu.Unlock()
	// Close even if cancellation happened immediately after commit. Initial
	// handshakes are included; no Channel is required to find their transport.
	for _, s := range sessions {
		c.endPlayerControlSession(s)
		if s.transport != nil {
			_ = s.transport.CloseNow()
		}
	}
	for _, s := range sessions {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-s.done:
		}
	}
	c.mu.Lock()
	c.refreshPairingLocked()
	c.mu.Unlock()
	return result, ctx.Err()
}
