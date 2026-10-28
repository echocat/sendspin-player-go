package sendspin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/wire"
)

var errHandshakeWriteFault = errors.New("test: injected handshake transport write failure")

// Fault injection is below the real WebSocket implementation, not a replacement
// of wire.Handshake/Channel. Arm only after the HTTP upgrade, at a peer-controlled
// sequence boundary where the next SDK data write is known. A prefix is a
// net.Conn.Write unit, not a TCP packet. The post-buffer error waits until the
// complete WebSocket data message, independently tracked and confirmed by the peer.
type handshakeFaultConn struct {
	net.Conn
	ctx               context.Context
	mu                sync.Mutex
	armed             bool
	started           bool
	mode              string
	inspect           func()
	triggered, closed chan struct{}
	closeOnce         sync.Once
	forwarded         int
	writeDeadline     time.Time
	deadlineChanged   chan struct{}
	frame             writeFaultFrame
	upgrading         bool
	armOnUpgrade      bool
	upgradeHeader     []byte
}

// Track the actual RFC 6455 carrier boundary without assuming that one Write
// contains a header, payload, TCP packet or even an entire WebSocket frame.
type writeFaultFrame struct {
	header            []byte
	remaining         uint64
	payload, fin      bool
	maskKnown, masked bool
}

func (f *writeFaultFrame) consume(data []byte) bool {
	for len(data) > 0 {
		if !f.payload {
			f.header = append(f.header, data[0])
			data = data[1:]
			if len(f.header) < 2 {
				continue
			}
			f.maskKnown, f.masked = true, f.header[1]&128 != 0
			size := int(f.header[1] & 127)
			needed := 2
			if size == 126 {
				needed += 2
			} else if size == 127 {
				needed += 8
			}
			if f.header[1]&128 != 0 {
				needed += 4
			}
			if len(f.header) < needed {
				continue
			}
			f.fin = f.header[0]&128 != 0
			f.remaining = uint64(size)
			if size == 126 {
				f.remaining = uint64(binary.BigEndian.Uint16(f.header[2:4]))
			} else if size == 127 {
				f.remaining = binary.BigEndian.Uint64(f.header[2:10])
			}
			f.payload = true
		} else {
			n := min(uint64(len(data)), f.remaining)
			data = data[int(n):]
			f.remaining -= n
		}
		if f.payload && f.remaining == 0 {
			if f.fin {
				return true
			}
			f.payload = false
			f.header = nil
		}
	}
	return false
}

func (c *handshakeFaultConn) arm(mode string, inspect func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = true
	c.started = false
	c.frame = writeFaultFrame{}
	c.mode = mode
	c.inspect = inspect
}
func (c *handshakeFaultConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	if c.upgrading {
		c.mu.Unlock()
		n, err := c.Conn.Write(data)
		c.mu.Lock()
		c.upgradeHeader = append(c.upgradeHeader, data[:n]...)
		if bytes.Contains(c.upgradeHeader, []byte("\r\n\r\n")) {
			if !bytes.HasPrefix(c.upgradeHeader, []byte("HTTP/1.1 101 ")) {
				c.mu.Unlock()
				return n, errors.New("test: expected a successful HTTP upgrade before arming")
			}
			c.upgrading = false
			c.upgradeHeader = nil
			if c.armOnUpgrade {
				c.armed = true
			}
		}
		c.mu.Unlock()
		return n, err
	}
	armed, mode, inspect := c.armed, c.mode, c.inspect
	first := !c.started
	if armed {
		c.started = true
		if mode != "after-buffer" {
			c.armed = false
		}
	}
	c.mu.Unlock()
	if !armed {
		return c.Conn.Write(data)
	}
	if first {
		inspect()
		close(c.triggered)
	}
	if mode == "cancel" || mode == "deadline" {
		return 0, c.blockWrite()
	}
	if mode == "zero" {
		return 0, errHandshakeWriteFault
	}
	part := data
	if mode == "prefix" {
		part = data[:max(1, len(data)/2)]
	}
	n, err := c.Conn.Write(part)
	c.mu.Lock()
	c.forwarded += n
	complete := false
	if mode == "after-buffer" {
		complete = c.frame.consume(part[:n])
		if complete {
			c.armed = false
		}
	}
	c.mu.Unlock()
	if err != nil {
		return n, err
	}
	if mode == "pass" {
		return n, nil
	}
	if mode == "after-buffer" && !complete {
		return n, nil
	}
	return n, errHandshakeWriteFault
}

// Wrap accepted sockets before net/http sees them. The HTTP 101 response is
// explicitly passed through: even the client/init fault starts only afterward.
type handshakeFaultListener struct {
	net.Listener
	wrap func(net.Conn) net.Conn
}

func (l handshakeFaultListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.wrap(conn), nil
}
func (c *handshakeFaultConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *handshakeFaultConn) SetWriteDeadline(at time.Time) error {
	c.mu.Lock()
	c.writeDeadline = at
	if c.deadlineChanged != nil {
		close(c.deadlineChanged)
	}
	c.deadlineChanged = make(chan struct{})
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(at)
}
func (c *handshakeFaultConn) blockWrite() error {
	// A net.Conn fixture must honor changing write deadlines as well as Close;
	// otherwise a simulated blocked Write could manufacture a library deadlock.
	for {
		c.mu.Lock()
		deadline, changed := c.writeDeadline, c.deadlineChanged
		c.mu.Unlock()
		var timer *time.Timer
		var due <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(max(0, time.Until(deadline)))
			due = timer.C
		}
		var err error
		select {
		case <-c.ctx.Done():
			err = c.ctx.Err()
		case <-c.closed:
			err = net.ErrClosed
		case <-due:
			err = &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
		case <-changed:
		}
		if timer != nil {
			timer.Stop()
		}
		if err != nil {
			return err
		}
	}
}

func (p *handshakePeer) readWriteTarget(encrypted bool) (string, error) {
	if encrypted {
		kind, _, err := p.readSecure()
		return kind, err
	}
	typ, raw, err := p.conn.Read(p.ctx)
	if err != nil {
		return "", err
	}
	if typ != websocket.MessageText {
		return "", errors.New("write target was not an initial text message")
	}
	kind, _, err := peerMessage(raw)
	return kind, err
}
func (p *handshakePeer) closedAfterWriteFault(expected string, encrypted bool) (bool, error) {
	observed := false
	for tries := 0; tries < 3; tries++ {
		// Read errors from a partial WebSocket frame are expected. A complete
		// data message must instead be the one write whose buffer was forwarded.
		typ, raw, err := p.conn.Read(p.ctx)
		if err != nil {
			if p.ctx.Err() != nil {
				return observed, errors.New("peer deadline expired instead of failed-write cleanup")
			}
			return observed, nil
		}
		if observed {
			return observed, errors.New("client sent additional data after a failed write")
		}
		if encrypted {
			if typ != websocket.MessageBinary {
				return observed, errors.New("client sent cleartext after transport write failure")
			}
			raw, err = p.in.Decrypt(nil, nil, raw)
			if err != nil {
				return observed, errors.New("client used unexpected keys after write failure")
			}
			if len(raw) == 0 || raw[0] != 0 {
				return observed, errors.New("unexpected client binary reply after failed write")
			}
			raw = raw[1:]
		} else if typ != websocket.MessageText {
			return observed, errors.New("unexpected binary initial reply after failed write")
		}
		kind, _, err := peerMessage(raw)
		if err != nil || kind != expected {
			return observed, errors.New("wrong application message after failed write")
		}
		observed = true
	}
	return observed, errors.New("write-failure receive drain did not terminate")
}

func (p *handshakePeer) fragmentedHelloAfterFault(pass bool) (int, error) {
	var body []byte
	for count := 0; count < 4; {
		typ, packet, err := p.conn.Read(p.ctx)
		if err != nil {
			if p.ctx.Err() != nil {
				return count, errors.New("peer deadline expired during fragmented-write cleanup")
			}
			if !pass && count > 0 {
				return count, nil
			}
			return count, errors.New("fragmented positive control did not finish")
		}
		if typ != websocket.MessageBinary {
			return count, errors.New("unexpected cleartext during fragmented write")
		}
		plain, err := p.in.Decrypt(nil, nil, packet)
		if err != nil {
			return count, err
		}
		if len(plain) < 3 || plain[0] != 1 {
			return count, errors.New("expected authenticated fragment, not a new message")
		}
		flags := plain[1]
		offset := 2
		if count == 0 {
			if flags != 2 || plain[2] != 0 {
				return count, errors.New("invalid first JSON fragment")
			}
			offset = 3
		} else if flags != 0 && flags != 1 {
			return count, errors.New("invalid continuation fragment")
		}
		count++
		if pass {
			body = append(body, plain[offset:]...)
		}
		if flags&1 != 0 {
			if !pass {
				return count, errors.New("logical message completed after failed continuation write")
			}
			kind, _, err := peerMessage(body)
			if err != nil || kind != "client/hello" {
				return count, errors.New("fragmented positive control was not client/hello")
			}
			return count, nil
		}
	}
	return 4, errors.New("fragmented write exceeded the bounded fixture sequence")
}

type writeBoundarySnapshot struct {
	s                *session
	category, record string
	admitted         bool
	mode             string
	modeUsers        int
}

func runHandshakeWriteFault(t *testing.T, suite, stage, mode string) {
	t.Helper()
	runDirectionalHandshakeWriteFault(t, suite, stage, mode, "outbound")
}

func runDirectionalHandshakeWriteFault(t *testing.T, suite, stage, mode, direction string) {
	t.Helper()
	testCtx, testCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer testCancel()
	runCtx, runCancel := context.WithCancel(testCtx)
	defer runCancel()
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	serverCredentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := Secret{127}
	credentials.Records = []PairingRecord{{ServerID: serverCredentials.ClientID(), PSK: key}}
	credentials.LastPlaybackServer = serverCredentials.ClientID()
	store := &controlCountingStore{}
	if err = store.Save(testCtx, credentials); err != nil {
		t.Fatal(err)
	}
	before := store.Saves.Load()
	var fault *handshakeFaultConn
	var inspect func()
	var sentFragments, logicalHello atomic.Int32
	ready := make(chan struct{})
	name := "Handshake write faults"
	if stage == "fragmented-hello" {
		name = strings.Repeat("f", 180000)
	}
	handshakeTimeout := 2 * time.Second
	if mode == "deadline" {
		handshakeTimeout = 500 * time.Millisecond
	}
	client, err := New(testCtx, Config{Name: name, Store: store, Roles: []Role{Controller}, AllowUnpaired: true, CipherSuite: suite, HandshakeTimeout: handshakeTimeout,
		Observe: func(message diagnostic.Message) {
			if stage != "fragmented-hello" || message.Direction != diagnostic.Sent {
				return
			}
			// Already closed before the peer can send server/hello. Besides the
			// protocol ordering, this synchronizes the accepted socket pointer.
			<-ready
			if message.Reassembled && message.Type == "client/hello" {
				logicalHello.Add(1)
			}
			if message.HasMessageID && message.MessageID == 1 && !message.Reassembled && sentFragments.Add(1) == 1 {
				// Only arm the transport; no blocking or SDK call from an observer.
				fault.arm(mode, inspect)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, err := ecdhPeerKey(credentials.PrivateKey[:])
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append([]byte("sendspin-psk-id-v1"), key[:]...))
	peer := &handshakePeer{ctx: testCtx, suite: suite, clientKey: clientPublic, serverKey: serverCredentials.PrivateKey[:], serverID: serverCredentials.ClientID(), pskID: base64.RawURLEncoding.EncodeToString(hash[:]), psk: key[:]}
	var boundary writeBoundarySnapshot
	var targetObserved bool // Read only after peerDone joins the peer handler.
	var receivedFragments int
	inspect = func() {
		client.mu.Lock()
		defer client.mu.Unlock()
		boundary.mode, boundary.modeUsers = client.mode, client.modeUsers
		for s := range client.sessions {
			boundary.s = s
			boundary.record = s.recordServerID
			boundary.admitted = s.admitted
			if s.channel != nil {
				boundary.category = s.channel.Category
			}
		}
	}
	peerDone := make(chan error, 1)
	runPeer := func(conn *websocket.Conn) {
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(65535)
		peer.conn = conn
		select {
		case <-ready:
		case <-testCtx.Done():
			peerDone <- testCtx.Err()
			return
		}
		script := func() (string, bool, error) {
			if stage == "client-init" {
				return "client/init", false, nil
			}
			if strings.HasPrefix(stage, "rekey-") {
				if err := peer.establish(); err != nil {
					return "", false, err
				}
				peer.psk = credentials.PairingPSK[:]
				hash := sha256.Sum256(append([]byte("sendspin-psk-id-v1"), peer.psk...))
				peer.pskID = base64.RawURLEncoding.EncodeToString(hash[:])
				hs, raw, err := peer.noise1(peer.hash, handshakeFault{body: `{"psk_id":"$PSK","psk_category":"pr"}`})
				if err != nil {
					return "", false, err
				}
				if stage == "rekey-noise2" {
					fault.arm(mode, inspect)
					return "noise/handshake", true, peer.sendSecure(raw)
				}
				if err = peer.sendSecure(raw); err != nil {
					return "", false, err
				}
				if err = peer.noise2(hs, true); err != nil {
					return "", false, err
				}
				fault.arm(mode, inspect)
				return "client/state", true, peer.sendSecure(peerJSON("server/activate", map[string]any{"activities": []string{}, "active_roles": []string{"controller@v1"}}))
			}
			init, err := peer.readClientInit()
			if err != nil {
				return "", false, err
			}
			prologue, err := peer.initialPrologue(init)
			if err != nil {
				return "", false, err
			}
			hs, raw, err := peer.noise1(prologue, handshakeFault{})
			if err != nil {
				return "", false, err
			}
			if stage == "initial-noise2" {
				fault.arm(mode, inspect)
				return "noise/handshake", false, peer.writeText(raw)
			}
			if err = peer.writeText(raw); err != nil {
				return "", false, err
			}
			if err = peer.noise2(hs, false); err != nil {
				return "", false, err
			}
			if stage == "client-hello" || stage == "fragmented-hello" {
				if stage != "fragmented-hello" {
					fault.arm(mode, inspect)
				}
				return "client/hello", true, peer.sendSecure(peerJSON("server/hello", map[string]any{"name": "Write failure probe"}))
			}
			if err = peer.sendSecure(peerJSON("server/hello", map[string]any{"name": "Write failure probe"})); err != nil {
				return "", false, err
			}
			kind, _, err := peer.readSecure()
			if err != nil {
				return "", false, err
			}
			if kind != "client/hello" {
				return "", false, errors.New("missing initial hello before state write")
			}
			fault.arm(mode, inspect)
			return "client/state", true, peer.sendSecure(peerJSON("server/activate", map[string]any{"activities": []string{}, "active_roles": []string{"controller@v1"}}))
		}
		expected, encrypted, err := script()
		if err == nil {
			if stage == "fragmented-hello" {
				receivedFragments, err = peer.fragmentedHelloAfterFault(mode == "pass")
				if err == nil && mode == "pass" {
					err = conn.CloseNow()
				}
			} else if mode == "pass" {
				var kind string
				kind, err = peer.readWriteTarget(encrypted)
				if err == nil && kind != expected {
					err = errors.New("positive control wrote a different message at the injection boundary")
				}
				if err == nil {
					err = conn.CloseNow()
				}
			} else {
				targetObserved, err = peer.closedAfterWriteFault(expected, encrypted)
			}
		}
		peerDone <- err
	}
	var conn *websocket.Conn
	var handlerDone chan struct{}
	switch direction {
	case "outbound":
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				peerDone <- err
				return
			}
			runPeer(conn)
		}))
		defer server.Close()
		transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			fault = &handshakeFaultConn{Conn: conn, ctx: runCtx, triggered: make(chan struct{}), closed: make(chan struct{})}
			return fault, nil
		}}
		defer transport.CloseIdleConnections()
		conn, _, err = websocket.Dial(testCtx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		if stage == "client-init" {
			fault.arm(mode, inspect)
		}
	case "inbound":
		handlerDone = make(chan struct{})
		accepted := make(chan *handshakeFaultConn, 1)
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(handlerDone)
			client.ServeHTTP(w, r.WithContext(runCtx))
		}))
		server.Listener = handshakeFaultListener{Listener: server.Listener, wrap: func(conn net.Conn) net.Conn {
			wrapped := &handshakeFaultConn{Conn: conn, ctx: runCtx, triggered: make(chan struct{}), closed: make(chan struct{}), upgrading: true, armOnUpgrade: stage == "client-init", mode: mode, inspect: inspect}
			accepted <- wrapped
			return wrapped
		}}
		server.Start()
		defer server.Close()
		conn, _, err = websocket.Dial(testCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		fault = <-accepted
		go runPeer(conn)
	default:
		t.Fatal("unknown fixture connection direction")
	}
	defer func() { _ = conn.CloseNow() }()
	close(ready)
	controllerDone := make(chan struct{})
	go func() {
		defer close(controllerDone)
		select {
		case <-fault.triggered:
			if mode == "cancel" {
				runCancel()
			}
		case <-runCtx.Done():
		}
	}()
	// Outbound exercises the post-upgrade handler; inbound runs ServeHTTP,
	// including admission mode reservation and the real unmasked writer.
	// No public Config hook or alternate handshake path is introduced.
	var clientErr error
	if direction == "outbound" {
		clientErr = client.handle(runCtx, conn)
	} else {
		select {
		case <-handlerDone:
		case <-testCtx.Done():
			t.Fatal("inbound handler failed to release its reservation")
		}
		for clientErr == nil {
			select {
			case event := <-client.Events():
				if event.Kind == "disconnected" {
					clientErr = event.Err
				}
			case <-testCtx.Done():
				t.Fatal("inbound handler did not report its write failure")
			}
		}
	}
	runCancel()
	<-controllerDone
	if err := <-peerDone; err != nil {
		t.Fatal("scripted peer:", err)
	}
	if clientErr == nil || testCtx.Err() != nil {
		t.Fatal("failed or canceled write did not terminate the handler")
	}
	select {
	case <-fault.triggered:
	default:
		t.Fatal("fault boundary was never exercised")
	}
	if mode != "pass" && mode != "cancel" && mode != "deadline" && !errors.Is(clientErr, errHandshakeWriteFault) {
		t.Fatal("write error was replaced or swallowed", clientErr)
	}
	if mode == "cancel" && !errors.Is(clientErr, context.Canceled) {
		t.Fatal("write cancellation was not propagated", clientErr)
	}
	if mode == "deadline" && !errors.Is(clientErr, context.DeadlineExceeded) && !errors.Is(clientErr, os.ErrDeadlineExceeded) {
		t.Fatal("handshake write did not fail through its local deadline", clientErr)
	}
	if mode == "prefix" || mode == "after-buffer" {
		fault.mu.Lock()
		forwarded := fault.forwarded
		fault.mu.Unlock()
		if forwarded == 0 {
			t.Fatal("fault test never forwarded any prefix/buffer")
		}
	}
	if mode == "after-buffer" && stage != "fragmented-hello" && !targetObserved {
		t.Fatal("post-buffer failure did not deliver the complete target message")
	}
	if mode == "after-buffer" {
		fault.mu.Lock()
		known, masked := fault.frame.maskKnown, fault.frame.masked
		fault.mu.Unlock()
		if !known || masked != (direction == "outbound") {
			t.Fatal("fault did not exercise the expected WebSocket writer masking", direction, known, masked)
		}
	}
	if (mode == "zero" || mode == "prefix" || mode == "cancel" || mode == "deadline") && targetObserved {
		t.Fatal("pre-completion fault unexpectedly delivered a complete target message")
	}
	if stage == "fragmented-hello" {
		wantFragments, wantSent, wantLogical := 1, int32(1), int32(0)
		if mode == "after-buffer" {
			wantFragments = 2
		}
		if mode == "pass" {
			wantFragments, wantSent, wantLogical = 3, 3, 1
		}
		if receivedFragments != wantFragments || sentFragments.Load() != wantSent || logicalHello.Load() != wantLogical {
			t.Fatal("wrong partial-write/observation boundary", receivedFragments, sentFragments.Load(), logicalHello.Load())
		}
	}
	if boundary.s == nil {
		t.Fatal("fault did not capture the in-flight session")
	}
	if direction == "inbound" && (boundary.mode != "inbound" || boundary.modeUsers != 1) {
		t.Fatal("inbound fault did not exercise the real reserved connection mode")
	}
	wantRecord, wantCategory := serverCredentials.ClientID(), "lt"
	if stage == "client-init" {
		wantRecord, wantCategory = "", ""
	}
	if stage == "initial-noise2" {
		wantCategory = ""
	}
	if stage == "rekey-state" {
		wantRecord, wantCategory = "", "pr"
	}
	if boundary.record != wantRecord || boundary.category != wantCategory {
		t.Fatal("wrong key/record state at the actual write boundary", stage)
	}
	if mode != "pass" && stage == "rekey-noise2" && (boundary.s.channel.Category != "lt" || boundary.s.recordServerID != serverCredentials.ClientID()) {
		t.Fatal("failed rekey committed the candidate category or released old-record protection")
	}
	if strings.HasPrefix(stage, "rekey-") && !boundary.admitted {
		t.Fatal("rekey fault did not run on an admitted connection")
	}
	persisted, err := store.Load(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	if store.Saves.Load() != before || !reflect.DeepEqual(persisted, credentials) {
		t.Fatal("write failure or canceled exchange changed stored identity, records or history")
	}
	client.mu.Lock()
	released := client.active == nil && len(client.sessions) == 0 && client.modeUsers == 0
	client.mu.Unlock()
	if !released {
		t.Fatal("failed write leaked active/provisional session state")
	}
}

func TestHandshakeAndRekeyWriteFailuresDoNotCommitOrContinue(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, stage := range []string{"client-init", "initial-noise2", "client-hello", "initial-state", "rekey-noise2", "rekey-state"} {
			for _, mode := range []string{"zero", "prefix", "after-buffer", "cancel", "pass"} {
				t.Run(suite+"/"+stage+"/"+mode, func(t *testing.T) { runHandshakeWriteFault(t, suite, stage, mode) })
			}
		}
	}
}

func TestHandshakeWriteDeadlinesReleaseStateAndKeepOldBindings(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, stage := range []string{"client-init", "initial-noise2", "rekey-noise2"} {
			t.Run(suite+"/"+stage, func(t *testing.T) { runHandshakeWriteFault(t, suite, stage, "deadline") })
		}
	}
}

func TestFragmentedHelloWriteFailureNeverCompletesOrReportsLogicalSend(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, mode := range []string{"zero", "prefix", "after-buffer", "cancel", "pass"} {
			t.Run(suite+"/"+mode, func(t *testing.T) { runHandshakeWriteFault(t, suite, "fragmented-hello", mode) })
		}
	}
}

func TestInboundHandshakeAndRekeyWriteFailuresDoNotCommitOrContinue(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, stage := range []string{"client-init", "initial-noise2", "client-hello", "initial-state", "rekey-noise2", "rekey-state"} {
			for _, mode := range []string{"zero", "prefix", "after-buffer", "cancel", "pass"} {
				t.Run(suite+"/"+stage+"/"+mode, func(t *testing.T) { runDirectionalHandshakeWriteFault(t, suite, stage, mode, "inbound") })
			}
		}
	}
}

func TestInboundHandshakeWriteDeadlinesReleaseStateAndKeepOldBindings(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, stage := range []string{"client-init", "initial-noise2", "rekey-noise2"} {
			t.Run(suite+"/"+stage, func(t *testing.T) { runDirectionalHandshakeWriteFault(t, suite, stage, "deadline", "inbound") })
		}
	}
}

func TestInboundFragmentedHelloWriteFailureNeverCompletesOrReportsLogicalSend(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, mode := range []string{"zero", "prefix", "after-buffer", "cancel", "pass"} {
			t.Run(suite+"/"+mode, func(t *testing.T) { runDirectionalHandshakeWriteFault(t, suite, "fragmented-hello", mode, "inbound") })
		}
	}
}

func TestWriteFaultTrackerHandlesMaskedSplitAndExtendedFrames(t *testing.T) {
	frame := func(size int, masked, final bool, opcode byte) []byte {
		first := opcode
		if final {
			first |= 128
		}
		out := []byte{first, 0}
		switch {
		case size < 126:
			out[1] = byte(size)
		case size <= 65535:
			out[1] = 126
			out = binary.BigEndian.AppendUint16(out, uint16(size))
		default:
			out[1] = 127
			out = binary.BigEndian.AppendUint64(out, uint64(size))
		}
		if masked {
			out[1] |= 128
			out = append(out, 1, 2, 3, 4)
		}
		return append(out, make([]byte, size)...)
	}
	for _, masked := range []bool{false, true} {
		for _, size := range []int{0, 10, 125, 126, 65535, 65536} {
			for _, continuation := range []bool{false, true} {
				data := frame(size, masked, true, 2)
				if continuation {
					data = append(frame(3, masked, false, 2), frame(size, masked, true, 0)...)
				}
				var tracker writeFaultFrame
				for i, b := range data {
					if got := tracker.consume([]byte{b}); got != (i == len(data)-1) {
						t.Fatal("fault tracker completed at the wrong byte boundary", masked, size, continuation, i)
					}
				}
				if !tracker.maskKnown || tracker.masked != masked {
					t.Fatal("fault tracker lost the actual mask bit", masked, size, continuation)
				}
			}
		}
	}
}
