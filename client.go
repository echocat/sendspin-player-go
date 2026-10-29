package sendspin

import (
	"context"
	"crypto/ecdh"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/sendspin-player-go/internal/wire"
	"github.com/flynn/noise"
)

var (
	// ErrProtocol identifies recognized message-shape, direction or sequencing
	// violations. It is the same sentinel used by the internal protocol layer;
	// transport/authentication and application Store/backend errors may differ.
	ErrProtocol        = protocol.ErrProtocol
	ErrNotConnected    = errors.New("sendspin: no admitted connection")
	ErrInactiveRole    = errors.New("sendspin: role or command is not active")
	ErrConnectionMode  = errors.New("sendspin: inbound and outbound modes cannot run simultaneously")
	ErrConnectionLimit = errors.New("sendspin: provisional connection limit")
)

type request struct {
	ctx   context.Context
	kind  string
	value any
	done  chan error
}

// Client represents one persistent device. Construct a separate Client and Store
// for each independent player. It is safe to call methods concurrently.
type Client struct {
	cfg         Config
	mu          sync.Mutex
	admission   sync.Mutex
	credentials Credentials
	active      *session
	sessions    map[*session]bool
	mode        string
	modeUsers   int
	events      chan Event
	pairWindow  pairingWindow
}

// New validates and copies configuration and loads (or creates and saves) the
// persistent identity. It does not start network/audio workers or discovery.
func New(ctx context.Context, config Config) (*Client, error) {
	if err := config.defaults(); err != nil {
		return nil, err
	}
	credentials, err := config.Store.Load(ctx)
	if errors.Is(err, ErrNoCredentials) {
		credentials, err = NewCredentials()
		if err == nil {
			err = ensureStaticPIN(&credentials, config.CodePairing)
		}
		if err == nil {
			err = config.Store.Save(ctx, credentials)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = validateCredentials(credentials); err != nil {
		return nil, err
	}
	if len(credentials.Records) > config.RecordCapacity {
		return nil, errors.New("sendspin: record capacity smaller than persisted records")
	}
	credentials = cloneCredentials(credentials)
	if config.CodePairing != nil && config.CodePairing.Static && credentials.StaticPairingPIN == nil {
		if err = ensureStaticPIN(&credentials, config.CodePairing); err != nil {
			return nil, err
		}
		if err = config.Store.Save(ctx, cloneCredentials(credentials)); err != nil {
			return nil, err
		}
	}
	c := &Client{cfg: config, credentials: credentials, sessions: map[*session]bool{}, events: make(chan Event, 64)}
	c.logger().Debug("Sendspin client initialized")
	return c, nil
}

// ClientID returns the persistent public Curve25519 identity (not a secret).
func (c *Client) ClientID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.credentials.ClientID() }

// PairingToken returns the confidential operator token. Expose it only through
// an intentional local provisioning interface, never ordinary logs or telemetry.
func (c *Client) PairingToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credentials.PairingToken()
}

// Events remains open for the reusable lifetime of the Client.
func (c *Client) Events() <-chan Event { return c.events }
func (c *Client) emit(e Event) {
	select {
	case c.events <- e:
	default:
	}
}

func (c *Client) enter(mode string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.modeUsers > 0 && (c.mode != mode || mode == "outbound") {
		return ErrConnectionMode
	}
	c.mode = mode
	c.modeUsers++
	return nil
}
func (c *Client) leave() {
	c.mu.Lock()
	c.modeUsers--
	if c.modeUsers == 0 {
		c.mode = ""
	}
	c.mu.Unlock()
}

// Connect runs one client-initiated session until cancellation or disconnect.
// Use Run for reconnect with backoff. The endpoint must use ws://: Sendspin's
// authenticated encryption is inside the WebSocket, not TLS.
func (c *Client) Connect(ctx context.Context, endpoint string) error {
	if err := c.enter("outbound"); err != nil {
		return err
	}
	defer c.leave()
	return c.connect(ctx, endpoint)
}
func (c *Client) connect(ctx context.Context, endpoint string) error {
	if err := validateEndpoint(endpoint); err != nil {
		return err
	}
	dial, cancel := context.WithTimeout(ctx, c.cfg.HandshakeTimeout)
	// A valid ws:// URL must not redirect into TLS (or a different endpoint).
	// The WebSocket library otherwise follows HTTP and WebSocket redirects.
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, _, err := websocket.Dial(dial, endpoint, &websocket.DialOptions{HTTPClient: httpClient})
	cancel()
	if err != nil {
		return err
	}
	return c.handle(ctx, conn)
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "ws" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("sendspin: expected ws://host/path endpoint")
	}
	return nil
}

// Run reconnects transient handshake, idle and playback failures with capped
// exponential backoff. A pairing-only drop terminates the attempt; resumption is
// operator-driven. Protocol and authorization failures are returned as well.
func (c *Client) Run(ctx context.Context, endpoint string) error {
	if err := c.enter("outbound"); err != nil {
		return err
	}
	defer c.leave()
	return c.run(ctx, endpoint)
}

func (c *Client) run(ctx context.Context, endpoint string) error {
	if err := validateEndpoint(endpoint); err != nil {
		return err
	}
	delay := time.Second
	for {
		start := time.Now()
		err := c.connect(ctx, endpoint)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !shouldReconnect(err) {
			return err
		}
		c.emit(Event{Kind: "disconnected", Err: err})
		wait, next := reconnectBackoff(delay, time.Since(start))
		c.logger().With("backoff_ms", wait.Milliseconds()).With("error_class", logErrorClass(err)).Debug("Retrying Sendspin connection")
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = next
	}
}

// Keep the final declared purpose across handle's cleanup without changing the
// underlying error identity. A bare transport error otherwise loses the fact
// that automatically reconnecting would resume an operator-owned pairing flow.
type pairingSessionEnded struct{ cause error }

func (e *pairingSessionEnded) Error() string { return e.cause.Error() }
func (e *pairingSessionEnded) Unwrap() error { return e.cause }

func shouldReconnect(err error) bool {
	if err == nil {
		return false
	}
	var goodbye *GoodbyeError
	if errors.As(err, &goodbye) {
		return goodbye.Reason == "restart"
	}
	var ended *pairingSessionEnded
	return !errors.Is(err, protocol.ErrProtocol) && !errors.As(err, &ended)
}

func reconnectBackoff(previous, connected time.Duration) (wait, next time.Duration) {
	if connected > 10*time.Second {
		previous = time.Second
	}
	return previous, min(previous*2, time.Minute)
}

// ServeHTTP accepts server-initiated WebSockets. It can be mounted in an existing
// HTTP server; advertising is managed by Serve or explicitly by the application.
// Browser origins are denied unless same-origin. Native Sendspin peers omit Origin.
func (c *Client) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS != nil {
		http.Error(w, "sendspin: use plain ws://; Noise provides authenticated encryption", http.StatusBadRequest)
		return
	}
	if err := c.enter("inbound"); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer c.leave()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if err = c.handle(r.Context(), conn); err != nil {
		c.emit(Event{Kind: "disconnected", Err: err})
	}
}

func (c *Client) handle(ctx context.Context, conn *websocket.Conn) (result error) {
	c.logger().Debug("Sendspin session started")
	defer func() { c.logger().With("error_class", logErrorClass(result)).Info("Sendspin session ended") }()
	defer func() { _ = conn.CloseNow() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := newSession(c, ctx, cancel)
	c.mu.Lock()
	if len(c.sessions) >= min(4, c.cfg.RecordCapacity-1) {
		c.mu.Unlock()
		return ErrConnectionLimit
	}
	c.sessions[s] = true
	key := c.credentials.PrivateKey
	c.mu.Unlock()
	defer func() {
		s.endPairing(true)
		c.mu.Lock()
		if c.pairWindow.owner == s {
			c.pairWindow = pairingWindow{}
		}
		delete(c.sessions, s)
		if c.active == s {
			c.active = nil
		}
		c.mu.Unlock()
		close(s.done)
	}()
	private, _ := ecdh.X25519().NewPrivateKey(key[:])
	hctx, hcancel := context.WithDeadline(ctx, s.activationDeadline)
	channel, err := wire.Handshake(hctx, conn, noise.DHKey{Private: key[:], Public: private.PublicKey().Bytes()}, c.cfg.CipherSuite, c.cfg.MaxMessageBytes, func(serverID string) wire.Resolve { return c.sessionResolver(s, serverID) }, c.cfg.Observe)
	hcancel()
	if err != nil {
		return fmt.Errorf("sendspin: handshake: %w", err)
	}
	s.commitChannel(channel)
	c.logger().Info("Sendspin encrypted channel established")
	err = s.run()
	c.mu.Lock()
	pairingOnly := c.mode == "outbound" && s.activated && s.rank == 1
	c.mu.Unlock()
	if err != nil && pairingOnly {
		return &pairingSessionEnded{cause: err}
	}
	return err
}

func (c *Client) resolver(serverID string) wire.Resolve {
	return c.sessionResolver(nil, serverID)
}

func (c *Client) sessionResolver(s *session, serverID string) wire.Resolve {
	return func(id, category string, initial bool) (wire.Selection, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		// A handshake has selected its PSK before Channel is returned. Protect
		// this record immediately, including while the connection is provisional.
		// A non-initial pr/sn selection must keep an old lt binding protected
		// until commitChannel: message 2 has not yet been sent successfully.
		if category == "pr" && pskID(c.credentials.PairingPSK) == id {
			if s != nil && initial {
				s.recordServerID = ""
			}
			return wire.Selection{PSK: append([]byte(nil), c.credentials.PairingPSK[:]...), Category: "pr"}, nil
		}
		if category == "sn" && pskID(sentinelPSK) == id {
			if s != nil && initial {
				s.recordServerID = ""
			}
			return wire.Selection{PSK: append([]byte(nil), sentinelPSK[:]...), Category: "sn"}, nil
		}
		if category == "lt" {
			for _, r := range c.credentials.Records {
				if pskID(r.PSK) == id {
					if r.ServerID != serverID {
						return wire.Selection{}, protocol.ErrProtocol
					}
					if s != nil {
						s.recordServerID = serverID
					}
					return wire.Selection{PSK: append([]byte(nil), r.PSK[:]...), Category: "lt"}, nil
				}
			}
		}
		if initial {
			if s != nil {
				s.recordServerID = ""
			}
			return wire.Selection{PSK: append([]byte(nil), sentinelPSK[:]...), Category: "sn"}, nil
		}
		return wire.Selection{}, protocol.ErrProtocol
	}
}

// commitChannel releases an old record only after the new transport keys have
// been installed. Resolver selection alone is earlier: message 2 may still
// block/fail. Initial long-term selection is protected immediately by the
// resolver, before Handshake returns a Channel.
func (s *session) commitChannel(channel *wire.Channel) {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	s.channel = channel
	s.recordServerID = ""
	if channel.Category == "lt" {
		s.recordServerID = channel.ServerID
	}
}

func (c *Client) update(ctx context.Context, f func(*Credentials) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneCredentials(c.credentials)
	if err := f(&next); err != nil {
		return err
	}
	if err := c.cfg.Store.Save(ctx, cloneCredentials(next)); err != nil {
		return err
	}
	c.credentials = next
	return nil
}

func (c *Client) request(ctx context.Context, kind string, value any) error {
	c.mu.Lock()
	s := c.active
	c.mu.Unlock()
	if s == nil {
		return ErrNotConnected
	}
	r := request{ctx: ctx, kind: kind, value: value, done: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrNotConnected
	case s.requests <- r:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrNotConnected
	case err := <-r.done:
		return err
	}
}

// Command sends a controller command only when that role/capability is active.
// Pointer arguments are copied; the caller may reuse the command after return.
func (c *Client) Command(ctx context.Context, command Command) error {
	command.Volume = copyPointer(command.Volume)
	command.Mute = copyPointer(command.Mute)
	command.PositionMS = copyPointer(command.PositionMS)
	command.OffsetMS = copyPointer(command.OffsetMS)
	return c.request(ctx, "command", command)
}
func copyPointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// LeaveGroup asks the admitted server to leave the current group.
func (c *Client) LeaveGroup(ctx context.Context) error { return c.request(ctx, "leave", nil) }

// SetAvailable declares the host's willingness to yield to Sendspin. Actual
// availability still depends on the SDK's clock/readiness gates.
func (c *Client) SetAvailable(ctx context.Context, available bool) error {
	return c.request(ctx, "available", available)
}

// SetVolume updates/persists a perceived volume in [0,100] without clearing mute.
func (c *Client) SetVolume(ctx context.Context, volume int) error {
	if volume < 0 || volume > 100 {
		return errors.New("sendspin: volume outside 0-100")
	}
	return c.request(ctx, "volume", volume)
}

// SetMuted updates/persists mute without changing the stored volume.
func (c *Client) SetMuted(ctx context.Context, muted bool) error {
	return c.request(ctx, "muted", muted)
}

// SetOutputDelay clamps to [0,5s], truncates to whole milliseconds, and persists
// the external post-port delay. It does not calibrate internal DAC latency.
func (c *Client) SetOutputDelay(ctx context.Context, delay time.Duration) error {
	return c.request(ctx, "delay", int(max(0, min(delay, 5*time.Second))/time.Millisecond))
}

// PreferFormat requests one of the immutable advertised formats. The server
// selects/changes the actual stream format; acceptance does not open hardware.
func (c *Client) PreferFormat(ctx context.Context, format codec.Format) error {
	return c.request(ctx, "format", format)
}

// GoodbyeError preserves the validated reason for a graceful session refusal or
// disconnect. Use errors.As; errors from custom Stores/backends remain wrapped.
type GoodbyeError struct{ Reason string }

func (e *GoodbyeError) Error() string { return "sendspin: disconnected: " + e.Reason }
