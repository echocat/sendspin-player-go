package sendspin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/echocat/sendspin-player-go/internal/monotime"
)

var ErrPairingDisabled = errors.New("sendspin: PIN pairing is disabled")
var ErrPairingActive = errors.New("sendspin: a pairing attempt is active")

// PairingPIN is a secret. Formatting and JSON redact it; Value explicitly
// returns contiguous digits for an intentional local operator interface.
type PairingPIN struct{ value string }

// Grouped formats the operator presentation (3-3 or 4-4), never the PAKE input.
func (p PairingPIN) Grouped() string {
	switch len(p.value) {
	case 6:
		return p.value[:3] + "-" + p.value[3:]
	case 8:
		return p.value[:4] + "-" + p.value[4:]
	}
	return ""
}

func (p PairingPIN) Value() string              { return p.value }
func (PairingPIN) String() string               { return "[REDACTED PIN]" }
func (PairingPIN) GoString() string             { return "[REDACTED PIN]" }
func (PairingPIN) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, "[REDACTED PIN]") }
func (PairingPIN) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED PIN]"`), nil }

// PairingDisplay transfers presentation to the application's display. While
// Active it replaces artwork/other output using that same display, without
// changing stream timelines or availability. No speaker output is generated.
type PairingDisplay struct {
	Active   bool
	PIN      PairingPIN
	ServerID string
	Round    uint32
}

// CodePairingConfig opts into static PIN pairing or dynamic displayed PINs.
// Automatic is an opt-in policy for the dynamic method, not a new wire method.
// RC1 allows at most one code method in hello; Static cannot be combined with
// Display or Automatic. Without Automatic, existing window/round behavior stays
// unchanged.
//
// Display is called synchronously by the session actor, must return promptly and
// honor its context, and must not call Client methods. Copy/post the event to a
// UI thread; Active=false hides it. It is not a logging/telemetry callback.
// If shared with artwork or another role's output, the host owns display takeover
// and restoration. Without Display, automatic-mode presentation uses
// [Client.CurrentPairingPIN] and [Client.PairingState].
//
// # Automatic admission and protocol limits
//
// Automatic accepts dynamic PIN attempts only while no durable server records
// exist. A disconnected/offline paired server does not reopen admission; an
// authenticated server/unpair removing the last record does. No application loop
// opening or renewing a pairing window is needed. Held attempts receive standard
// client/pair-pending. At most one pairing attempt, including PSK pairing, is active.
//
// Sendspin 1.0.0-rc1 requires dynamic digit codes to have six digits, derived from
// the Noise binding and two CSPRNG nonces. No PIN exists before the server starts
// pairing and supplies its nonce. The same PIN is used across an attempt's retry
// rounds; the entire attempt has a two-minute deadline, including callbacks,
// writes and persistence. Expiry or the fifth failed confirmation aborts it;
// another server pairing activation is needed for a new attempt. New attempts
// use fresh nonces/transcripts, but their digits may coincidentally be identical.
// Old transcript proofs/finalizes cannot complete a new attempt. Independent
// eight-digit PINs, guaranteed different digits and ten-minute device-PIN rotation
// are not supported by this standard method. Static codes remain fixed,
// eight-digit and gesture-window-gated; automatically reopening their window is
// not a standard alternative.
//
// Pairing PSK/token remains supported independently of PIN admission. Successful
// PIN pairing neither consumes nor disables it. Only [Client.RestartPairing]
// replaces the bootstrap PSK; anyone holding the current token can still pair.
//
// # Automatic-mode rate protection
//
// Protection is device-wide, not partitioned by connection, address or server ID:
//   - Save a round before emitting its PIN. An emitted round remains consumed
//     after malformed input, cancellation, timeout or disconnect. A verified
//     server_kc resets the counter. Otherwise, after 20 rounds a deliberate
//     operator release is required, using [Client.OpenPairingWindow] or the
//     confirmed administrator reset. Cooldown, new PIN, reconnect and process
//     restart never release that lockout.
//   - Count a wrong authentication only when a correctly sequenced, well-formed
//     server_kc fails CPace verification. Wrong codes and forged confirmations
//     are indistinguishable. Malformed input, server-declared errors, timeouts,
//     cancellation and network loss are not proven wrong PINs.
//   - The fifth such failure durably starts a 30-second cooldown, resets the
//     short counter and aborts/hides the PIN. Earlier failures may retry the same
//     code. Durably completed pairing clears the short counter.
//   - Before emitting a valid PAKE response, persist an unresolved-authentication
//     reservation. If no verification outcome can be saved, including after a
//     disconnect or crash, conservatively wait 30 seconds before another attempt.
//     This does not count as a proven failure; its round remains consumed.
//   - A protection-write error suppresses the associated PIN/PAKE output, hides
//     the PIN and latches automatic admission closed for this process. Fix storage
//     before explicitly releasing the pause. Final trust requires a successful
//     atomic record save. Administrator reset retains short failures/cooldown
//     and turns an outstanding reservation into a cooldown.
//
// # Persistence and clocks
//
// Use a durable [Store], not [MemoryStore], for a device. Existing credential
// fields/records are retained when opting in; missing protection fields mean
// zero/false. Custom Stores must preserve all [Credentials] fields. Do not use
// older writers that discard them. No dynamic PIN or PAKE transcript is persisted.
// Deleting/rolling back storage or running competing Clients defeats protection.
//
// Live deadlines use the process-local monotonic clock; wall-clock jumps cannot
// shorten them. A persisted cooldown or unresolved reservation starts a fresh
// full 30-second wait on every process startup, regardless of downtime. Its marker
// is cleared atomically by the next admitted round, not by elapsed downtime.
// Repeated restarts may extend but cannot shorten the wait. Existing session
// ticks and public state/PIN reads reconcile deadlines; New starts no policy
// goroutine, timer or callback queue. Events are bounded, best-effort hints;
// refresh state/PIN while displaying and honor [PairingPINDisplay.ValidUntil].
type CodePairingConfig struct {
	Static  bool
	Display func(context.Context, PairingDisplay) error
	// Automatic offers dynamic six-digit pairing only while no server records
	// exist. Display is optional: CurrentPairingPIN provides explicit UI access.
	// The protocol's persistent 20-round operator lockout still applies.
	Automatic bool
}
type pairingWindow struct {
	expires  time.Time
	owner    *session
	failures int
}

func ensureStaticPIN(c *Credentials, cfg *CodePairingConfig) error {
	if cfg == nil || !cfg.Static || c.StaticPairingPIN != nil {
		return nil
	}
	var secret Secret
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	for i := range 8 {
		var b [1]byte
		for {
			if _, err := rand.Read(b[:]); err != nil {
				return err
			}
			if b[0] < 250 {
				break
			}
		}
		secret[i] = '0' + b[0]%10
	}
	c.StaticPairingPIN = &secret
	return nil
}

// StaticPairingPIN returns the per-device provisioned eight-digit PIN, only
// when explicitly enabled. Never print Value in routine logs or telemetry.
func (c *Client) StaticPairingPIN() (PairingPIN, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.CodePairing == nil || !c.cfg.CodePairing.Static || c.credentials.StaticPairingPIN == nil {
		return PairingPIN{}, false
	}
	return PairingPIN{string(c.credentials.StaticPairingPIN[:8])}, true
}

// OpenPairingWindow represents a deliberate LOCAL operator gesture. Applications
// must not expose it to unauthenticated network commands. It opens a five-minute
// static-PIN window and resets the persistent, device-wide dynamic round limit.
// The first static attempt binds the window to its connection. Active attempts
// must first be canceled; merely reconnecting/restarting never unlocks the limit.
// In Automatic mode this resumes an operator/storage pause or the 20-round
// lockout, without clearing failure counts or cooldowns; otherwise it is a no-op.
func (c *Client) OpenPairingWindow(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.cfg.CodePairing == nil {
		return ErrPairingDisabled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for s := range c.sessions {
		if s.pairingAttempt {
			return ErrPairingActive
		}
	}
	next := cloneCredentials(c.credentials)
	next.DynamicPairingRounds = 0
	if c.automaticPairing() {
		if c.pairingRestarting {
			return ErrPairingRestarting
		}
		if c.credentials.DynamicPairingRounds < 20 && !c.pairingPaused && !c.pairingBlocked {
			return nil // Not a mechanism for repeatedly resetting an active budget.
		}
	}
	if err := c.cfg.Store.Save(ctx, cloneCredentials(next)); err != nil {
		return err
	}
	c.credentials = next
	c.pairingPaused, c.pairingBlocked = false, false
	if c.automaticPairing() {
		c.pairingChangedLocked()
	}
	c.pairWindow = pairingWindow{expires: monotime.Now().Add(5 * time.Minute)}
	return nil
}

// CancelPairing closes the local window and aborts an active/pending attempt.
// With no admitted session it still closes the window and succeeds.
// Automatic mode is also paused for this process; OpenPairingWindow resumes it.
func (c *Client) CancelPairing(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.pairWindow = pairingWindow{}
	if c.automaticPairing() {
		c.pairingPaused = true
		c.pairingPIN = PairingPINDisplay{}
		c.pairingChangedLocked()
	}
	s := c.active
	c.mu.Unlock()
	if s == nil {
		return nil
	}
	return c.request(ctx, "pairing/cancel", nil)
}
func (s *session) closePairingWindow() {
	s.client.mu.Lock()
	if s.client.pairWindow.owner == s {
		s.client.pairWindow = pairingWindow{}
	}
	s.client.mu.Unlock()
}
