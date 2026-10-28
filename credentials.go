package sendspin

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Secret is a 256-bit secret. Formatting redacts it, serialization does not.
type Secret [32]byte

func (Secret) String() string                 { return "[REDACTED]" }
func (Secret) GoString() string               { return "[REDACTED]" }
func (Secret) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, "[REDACTED]") }
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(base64.RawURLEncoding.EncodeToString(s[:])), nil
}
func (s *Secret) UnmarshalText(b []byte) error {
	v, err := base64.RawURLEncoding.Strict().DecodeString(string(b))
	if err != nil || len(v) != 32 {
		return errors.New("sendspin: invalid secret encoding")
	}
	copy(s[:], v)
	return nil
}

// PairingRecord binds a long-term PSK to an authenticated server identity.
type PairingRecord struct {
	ServerID string `json:"server_id"`
	PSK      Secret `json:"psk"`
}

// Credentials are persisted as one transaction. Records are ordered oldest first.
type Credentials struct {
	PrivateKey         Secret          `json:"private_key"`
	PairingPSK         Secret          `json:"pairing_psk"`
	Records            []PairingRecord `json:"records"`
	LastPlaybackServer string          `json:"last_playback_server,omitempty"`
	OutputDelayMS      int             `json:"output_delay_ms"`
	Volume             int             `json:"volume"`
	Muted              bool            `json:"muted"`
}

func (Credentials) String() string   { return "[sendspin credentials REDACTED]" }
func (Credentials) GoString() string { return "[sendspin credentials REDACTED]" }
func (Credentials) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[sendspin credentials REDACTED]")
}

func NewCredentials() (Credentials, error) {
	var c Credentials
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return c, err
	}
	copy(c.PrivateKey[:], key.Bytes())
	if _, err = rand.Read(c.PairingPSK[:]); err != nil {
		return c, err
	}
	c.Volume = 100
	return c, nil
}

func (c Credentials) ClientID() string {
	key, _ := ecdh.X25519().NewPrivateKey(c.PrivateKey[:])
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
}

// PairingToken is confidential; expose it to the operator, never in routine logs.
func (c Credentials) PairingToken() string {
	pub, _ := base64.RawURLEncoding.DecodeString(c.ClientID())
	data := append(pub, c.PairingPSK[:]...)
	body := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data)
	return "SP:0" + strings.ReplaceAll(body, "2", "9")
}

// ParsePairingToken accepts operator input and ignores reserved trailing bytes.
func ParsePairingToken(token string) (clientID string, psk Secret, err error) {
	s := strings.TrimSpace(token)
	// Tokens are ASCII. Unicode case folding must not turn lookalikes into
	// valid alphabet characters, and base32's CR/LF skipping is not allowed
	// inside a token: only surrounding whitespace is operator leniency.
	for _, character := range s {
		if character > 127 || character == '\r' || character == '\n' {
			return "", psk, errors.New("sendspin: malformed pairing token")
		}
	}
	s = strings.TrimPrefix(strings.ToUpper(s), "SP:")
	if !strings.HasPrefix(s, "0") {
		return "", psk, errors.New("sendspin: expected version-0 pairing token")
	}
	body := strings.ReplaceAll(s[1:], "9", "2")
	body += strings.Repeat("=", (8-len(body)%8)%8)
	b, e := base32.StdEncoding.DecodeString(body)
	if e != nil || len(b) < 64 {
		return "", psk, errors.New("sendspin: malformed pairing token")
	}
	copy(psk[:], b[32:64])
	return base64.RawURLEncoding.EncodeToString(b[:32]), psk, nil
}

func pskID(psk Secret) string {
	b := append([]byte("sendspin-psk-id-v1"), psk[:]...)
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

var sentinelPSK = Secret(sha256.Sum256([]byte("sendspin-sentinel-psk-v1")))

var ErrNoCredentials = errors.New("sendspin: no credentials stored")

// Store implementations must atomically save the complete snapshot. A failed
// Save must not partially replace an existing snapshot. One Client owns a Store.
// Methods should honor context cancellation promptly; pairing persistence carries
// the attempt deadline. Client cannot preempt a Store that ignores its context.
// Load must return a stable snapshot. The SDK detaches Records at Load/Save
// boundaries; implementations must never mutate another caller's input slices.
// Save runs inside a serialized credential transaction and must not call Client
// methods (including Snapshot). A canceled operation is not distributed rollback.
type Store interface {
	Load(context.Context) (Credentials, error)
	Save(context.Context, Credentials) error
}

func validateCredentials(c Credentials) error {
	if c.PrivateKey == (Secret{}) || c.PairingPSK == (Secret{}) {
		return errors.New("sendspin: empty credentials")
	}
	if c.OutputDelayMS < 0 || c.OutputDelayMS > 5000 || c.Volume < 0 || c.Volume > 100 {
		return errors.New("sendspin: invalid persisted player state")
	}
	seen := map[string]bool{}
	for _, r := range c.Records {
		if !validID(r.ServerID) || r.PSK == (Secret{}) || seen[r.ServerID] {
			return fmt.Errorf("sendspin: invalid pairing record")
		}
		seen[r.ServerID] = true
	}
	return nil
}

func validID(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == 32 && len(s) == 43
}
