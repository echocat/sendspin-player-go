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

// CodePairingConfig opts into either static PIN pairing or dynamic displayed
// PINs. RC1 allows at most one code method in a Client's hello; both are supported
// by the SDK, but Static and Display cannot be configured together.
// Display is called synchronously by the session actor, must return promptly and
// honor its context, and must not call Client methods. Copy/post the event to a
// UI thread; Active=false hides it. It is not a logging/telemetry callback.
type CodePairingConfig struct {
	Static  bool
	Display func(context.Context, PairingDisplay) error
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
func (c *Client) OpenPairingWindow(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.cfg.CodePairing == nil {
		return ErrPairingDisabled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for s := range c.sessions {
		if s.pairingAttempt {
			return ErrPairingActive
		}
	}
	next := cloneCredentials(c.credentials)
	next.DynamicPairingRounds = 0
	if err := c.cfg.Store.Save(ctx, cloneCredentials(next)); err != nil {
		return err
	}
	c.credentials = next
	c.pairWindow = pairingWindow{expires: monotime.Now().Add(5 * time.Minute)}
	return nil
}

// CancelPairing closes the local window and aborts an active/pending attempt.
// With no admitted session it still closes the window and succeeds.
func (c *Client) CancelPairing(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.pairWindow = pairingWindow{}
	s := c.active
	c.mu.Unlock()
	if s == nil {
		return nil
	}
	return c.request(ctx, "pairing/cancel", nil)
}
func (s *session) closePairingWindow() {
	s.client.mu.Lock()
	s.client.pairWindow = pairingWindow{}
	s.client.mu.Unlock()
}
