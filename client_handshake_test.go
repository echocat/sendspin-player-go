package sendspin

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/internal/wire"
	"github.com/flynn/noise"
)

// This scripted peer implements its own framing/sequence and uses the same
// upstream Noise primitive as the SDK. It is not independent cryptographic or
// aiosendspin evidence. It never uses wire.Handshake/Channel on the peer side.
type handshakePeer struct {
	ctx                  context.Context
	conn                 *websocket.Conn
	suite                string
	clientKey, serverKey []byte
	serverID, pskID      string
	psk                  []byte
	hash                 []byte
	out, in              *noise.CipherState
	readMessage          func() (websocket.MessageType, []byte, error)
}

func (p *handshakePeer) readRaw() (websocket.MessageType, []byte, error) {
	if p.readMessage != nil {
		return p.readMessage()
	}
	return p.conn.Read(p.ctx)
}

type handshakeFault struct {
	name, phase, body                         string
	binary, tamper, wrongPrologue, wrongSuite bool
}

func handshakeFaults() []handshakeFault {
	return []handshakeFault{
		{name: "init-binary", phase: "init", body: `{"type":"server/init","payload":{"server_id":"$SERVER","version":1}}`, binary: true},
		{name: "init-wrong-direction", phase: "init", body: `{"type":"client/init","payload":{}}`},
		{name: "init-hello-too-early", phase: "init", body: `{"type":"server/hello","payload":{"name":"too early"}}`},
		{name: "init-invalid-json", phase: "init", body: `{"type":`},
		{name: "init-invalid-utf8", phase: "init", body: "{\"type\":\"server/init\",\"payload\":{\"server_id\":\"\xff\",\"version\":1}}"},
		{name: "init-null-payload", phase: "init", body: `{"type":"server/init","payload":null}`},
		{name: "init-missing-id", phase: "init", body: `{"type":"server/init","payload":{"version":1}}`},
		{name: "init-case-alias", phase: "init", body: `{"type":"server/init","payload":{"SERVER_ID":"$SERVER","version":1}}`},
		{name: "init-null-id", phase: "init", body: `{"type":"server/init","payload":{"server_id":null,"version":1}}`},
		{name: "init-short-id", phase: "init", body: `{"type":"server/init","payload":{"server_id":"AA","version":1}}`},
		{name: "init-padded-id", phase: "init", body: `{"type":"server/init","payload":{"server_id":"$SERVER=","version":1}}`},
		{name: "init-missing-version", phase: "init", body: `{"type":"server/init","payload":{"server_id":"$SERVER"}}`},
		{name: "init-null-version", phase: "init", body: `{"type":"server/init","payload":{"server_id":"$SERVER","version":null}}`},
		{name: "init-string-version", phase: "init", body: `{"type":"server/init","payload":{"server_id":"$SERVER","version":"1"}}`},
		{name: "init-fraction-version", phase: "init", body: `{"type":"server/init","payload":{"server_id":"$SERVER","version":1.5}}`},
		{name: "init-future-version", phase: "init", body: `{"type":"server/init","payload":{"server_id":"$SERVER","version":2}}`},
		{name: "before-noise-application", phase: "before-noise", body: `{"type":"server/activate","payload":{"activities":[]}}`},
		{name: "before-noise-bare-bytes", phase: "before-noise", body: "\x00\x01", binary: true},
		{name: "noise-null-payload", phase: "noise", body: `{"type":"noise/handshake","payload":null}`},
		{name: "noise-missing-data", phase: "noise", body: `{"type":"noise/handshake","payload":{}}`},
		{name: "noise-null-data", phase: "noise", body: `{"type":"noise/handshake","payload":{"data":null}}`},
		{name: "noise-array-data", phase: "noise", body: `{"type":"noise/handshake","payload":{"data":[]}}`},
		{name: "noise-case-alias", phase: "noise", body: `{"type":"noise/handshake","payload":{"DATA":"AA"}}`},
		{name: "noise-invalid-base64", phase: "noise", body: `{"type":"noise/handshake","payload":{"data":"AB"}}`},
		{name: "noise-padded-base64", phase: "noise", body: `{"type":"noise/handshake","payload":{"data":"AA=="}}`},
		{name: "noise-empty-bytes", phase: "noise", body: `{"type":"noise/handshake","payload":{"data":""}}`},
		{name: "noise-truncated-bytes", phase: "noise", body: `{"type":"noise/handshake","payload":{"data":"AA"}}`},
		{name: "noise-over-limit", phase: "noise", body: string(peerJSON("noise/handshake", map[string]string{"data": base64.RawURLEncoding.EncodeToString(make([]byte, 65536))}))},
		{name: "inner-invalid-json", phase: "inner", body: `{"psk_id":`},
		{name: "inner-invalid-utf8", phase: "inner", body: "{\"psk_id\":\"$PSK\",\"psk_category\":\"\xff\"}"},
		{name: "inner-array", phase: "inner", body: `[]`},
		{name: "inner-null", phase: "inner", body: `null`},
		{name: "inner-missing-id", phase: "inner", body: `{"psk_category":"lt"}`},
		{name: "inner-null-id", phase: "inner", body: `{"psk_id":null,"psk_category":"lt"}`},
		{name: "inner-short-id", phase: "inner", body: `{"psk_id":"AA","psk_category":"lt"}`},
		{name: "inner-padded-id", phase: "inner", body: `{"psk_id":"$PSK=","psk_category":"lt"}`},
		{name: "inner-missing-category", phase: "inner", body: `{"psk_id":"$PSK"}`},
		{name: "inner-null-category", phase: "inner", body: `{"psk_id":"$PSK","psk_category":null}`},
		{name: "inner-unknown-category", phase: "inner", body: `{"psk_id":"$PSK","psk_category":"future"}`},
		{name: "inner-case-category", phase: "inner", body: `{"psk_id":"$PSK","psk_category":"LT"}`},
		{name: "noise-tamper", phase: "inner", tamper: true},
		{name: "noise-wrong-prologue", phase: "inner", wrongPrologue: true},
		{name: "noise-wrong-suite", phase: "inner", wrongSuite: true},
	}
}

func (p *handshakePeer) writeText(raw []byte) error {
	return p.conn.Write(p.ctx, websocket.MessageText, raw)
}
func peerJSON(kind string, payload any) []byte {
	data, err := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{kind, payload})
	if err != nil {
		panic(err)
	}
	return data
}
func peerMessage(raw []byte) (kind string, payload json.RawMessage, err error) {
	var message struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	err = json.Unmarshal(raw, &message)
	if err == nil {
		_, err = exactAllowedKeys(raw, "type payload")
	}
	return message.Type, message.Payload, err
}
func (p *handshakePeer) readClientInit() (raw []byte, err error) {
	typ, raw, err := p.readRaw()
	if err != nil {
		return nil, err
	}
	kind, payload, err := peerMessage(raw)
	if err != nil || typ != websocket.MessageText || kind != "client/init" {
		return nil, errors.New("expected client/init as the only first data message")
	}
	var init struct {
		ID      string `json:"client_id"`
		Version int    `json:"version"`
		Suite   string `json:"suite"`
	}
	if json.Unmarshal(payload, &init) != nil || init.ID != base64.RawURLEncoding.EncodeToString(p.clientKey) || init.Version != 1 || init.Suite != p.suite {
		return nil, errors.New("client/init fields differ")
	}
	if err = checkOutgoingPayload(kind, payload); err != nil {
		return nil, err
	}
	return raw, nil
}
func (p *handshakePeer) expand(body string) []byte {
	return []byte(strings.NewReplacer("$SERVER", p.serverID, "$PSK", p.pskID).Replace(body))
}
func (p *handshakePeer) initialPrologue(init []byte) ([]byte, error) {
	// Whitespace, key order and unknown informational fields must remain in
	// the raw prologue. No SDK encoder/parser constructs this server/init.
	serverInit := p.expand(" {\n\"payload\":{\"version\":1,\"VERSION\":null,\"future\":[null,false],\"server_id\":\"$SERVER\"},\"type\":\"server/init\"} ")
	if err := p.writeText(serverInit); err != nil {
		return nil, err
	}
	return append(append([]byte(nil), init...), serverInit...), nil
}
func (p *handshakePeer) noise1(prologue []byte, fault handshakeFault) (*noise.HandshakeState, []byte, error) {
	cipher := noise.CipherChaChaPoly
	if (p.suite == wire.AES) != fault.wrongSuite {
		cipher = noise.CipherAESGCM
	}
	if fault.wrongPrologue {
		prologue = append(append([]byte(nil), prologue...), 0)
	}
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, cipher, noise.HashSHA256), Pattern: noise.HandshakeKK, Initiator: true, PresharedKey: p.psk, PresharedKeyPlacement: 2, StaticKeypair: noise.DHKey{Private: p.serverKey, Public: mustPeerPublic(p.serverKey)}, PeerStatic: p.clientKey, Prologue: prologue})
	if err != nil {
		return nil, nil, err
	}
	inner := p.expand(`{"psk_id":"$PSK","psk_category":"lt","PSK_ID":null,"future":[null,{"opaque":false}]}`)
	if fault.body != "" {
		inner = p.expand(fault.body)
	}
	data, _, _, err := hs.WriteMessage(nil, inner)
	if err != nil {
		return nil, nil, err
	}
	if fault.tamper {
		data[len(data)-1] ^= 1
	}
	return hs, peerJSON("noise/handshake", map[string]any{"data": base64.RawURLEncoding.EncodeToString(data), "DATA": nil, "future": []any{nil}}), nil
}
func mustPeerPublic(private []byte) []byte {
	// Credentials keys are generated/validated by the test setup.
	key, err := ecdhPeerKey(private)
	if err != nil {
		panic(err)
	}
	return key
}
func ecdhPeerKey(private []byte) ([]byte, error) {
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, err
	}
	return key.PublicKey().Bytes(), nil
}

func (p *handshakePeer) sendSecure(raw []byte) error {
	write := func(plain []byte) error {
		packet, err := p.out.Encrypt(nil, nil, plain)
		if err != nil {
			return err
		}
		return p.conn.Write(p.ctx, websocket.MessageBinary, packet)
	}
	if len(raw) <= 65518 {
		return write(append([]byte{0}, raw...))
	}
	// Independent RC1 fragment encoding, not protocol.Fragment. First frame
	// carries orig_type 0; subsequent frames carry only flags and JSON bytes.
	first := true
	for len(raw) > 0 {
		prefix := []byte{1, 0}
		if first {
			prefix = []byte{1, 2, 0}
		}
		n := min(len(raw), 65519-len(prefix))
		if n == len(raw) {
			prefix[1] |= 1
		}
		if err := write(append(prefix, raw[:n]...)); err != nil {
			return err
		}
		raw = raw[n:]
		first = false
	}
	return nil
}
func (p *handshakePeer) readSecure() (string, json.RawMessage, error) {
	typ, packet, err := p.readRaw()
	if err != nil {
		return "", nil, err
	}
	if typ != websocket.MessageBinary {
		return "", nil, errors.New("client sent cleartext in transport mode")
	}
	plain, err := p.in.Decrypt(nil, nil, packet)
	if err != nil {
		return "", nil, err
	}
	if len(plain) == 0 || plain[0] != 0 {
		return "", nil, errors.New("unexpected client binary data")
	}
	kind, payload, err := peerMessage(plain[1:])
	if err == nil {
		err = checkOutgoingPayload(kind, payload)
	}
	return kind, payload, err
}
func (p *handshakePeer) noise2(hs *noise.HandshakeState, rekey bool) error {
	var payload json.RawMessage
	for tries := 0; tries < 32; tries++ {
		var kind string
		var err error
		if rekey {
			kind, payload, err = p.readSecure()
		} else {
			var typ websocket.MessageType
			var raw []byte
			typ, raw, err = p.readRaw()
			if err == nil && typ != websocket.MessageText {
				return errors.New("initial Noise response was not text")
			}
			if err == nil {
				kind, payload, err = peerMessage(raw)
			}
		}
		if err != nil {
			return err
		}
		if rekey && kind == "client/time" {
			continue
		} // Valid old-key requests may cross.
		if kind != "noise/handshake" {
			return errors.New("unexpected application response during handshake")
		}
		if err = checkOutgoingPayload(kind, payload); err != nil {
			return err
		}
		var body struct {
			Data string `json:"data"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return err
		}
		data, err := base64.RawURLEncoding.Strict().DecodeString(body.Data)
		if err != nil {
			return err
		}
		plain, out, in, err := hs.ReadMessage(nil, data)
		if err != nil {
			return err
		}
		if !bytes.Equal(plain, []byte("{}")) {
			return errors.New("Noise message-2 payload was not the literal empty object")
		}
		p.out, p.in = out, in
		p.hash = bytes.Clone(hs.ChannelBinding())
		return nil
	}
	return errors.New("no Noise response within bounded old-key request drain")
}
func (p *handshakePeer) activate(initial bool) error {
	if initial {
		if err := p.sendSecure(peerJSON("server/hello", map[string]any{"name": "Scripted handshake peer"})); err != nil {
			return err
		}
		kind, _, err := p.readSecure()
		if err != nil {
			return err
		}
		if kind != "client/hello" {
			return errors.New("missing initial client/hello")
		}
	}
	if err := p.sendSecure(peerJSON("server/activate", map[string]any{"activities": []string{}, "active_roles": []string{}})); err != nil {
		return err
	}
	kind, _, err := p.readSecure()
	if err != nil {
		return err
	}
	if kind != "client/time" {
		return errors.New("activation did not resume core clock exchange")
	}
	return nil
}
func (p *handshakePeer) closedWithoutData(allowOldRequests bool) error {
	for tries := 0; tries < 32; tries++ {
		typ, packet, err := p.readRaw()
		if err != nil {
			if p.ctx.Err() != nil {
				return errors.New("peer deadline expired instead of client closure")
			}
			return nil
		}
		if allowOldRequests && typ == websocket.MessageBinary {
			plain, err := p.in.Decrypt(nil, nil, packet)
			if err == nil && len(plain) > 0 && plain[0] == 0 {
				kind, _, parseErr := peerMessage(plain[1:])
				if parseErr == nil && kind == "client/time" {
					continue
				}
			}
		}
		return errors.New("client sent further data after handshake failure")
	}
	return errors.New("client kept sending old-key requests after failed rekey")
}

func runHandshakePeer(t *testing.T, suite, direction string, timeout time.Duration, script func(*handshakePeer) error) {
	t.Helper()
	runConfiguredHandshakePeer(t, suite, direction, timeout, Config{Roles: []Role{Controller}}, script)
}

func runConfiguredHandshakePeer(t *testing.T, suite, direction string, timeout time.Duration, config Config, script func(*handshakePeer) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	serverCredentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := Secret{126}
	credentials.Records = []PairingRecord{{ServerID: serverCredentials.ClientID(), PSK: key}}
	credentials.LastPlaybackServer = serverCredentials.ClientID()
	store := &controlCountingStore{}
	if err = store.Save(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	before := store.Saves.Load()
	config.Name, config.Store, config.CipherSuite, config.HandshakeTimeout = "Handshake phase matrix", store, suite, timeout
	client, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, err := ecdhPeerKey(credentials.PrivateKey[:])
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append([]byte("sendspin-psk-id-v1"), key[:]...))
	peer := &handshakePeer{ctx: ctx, suite: suite, clientKey: clientPublic, serverKey: serverCredentials.PrivateKey[:], serverID: serverCredentials.ClientID(), pskID: base64.RawURLEncoding.EncodeToString(hash[:]), psk: key[:]}
	var clientErr, peerErr error
	if direction == "outbound" {
		done := make(chan error, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = conn.CloseNow() }()
			peer.conn = conn
			done <- script(peer)
		}))
		defer server.Close()
		clientErr = client.Connect(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
		peerErr = <-done
	} else {
		finished := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(finished); client.ServeHTTP(w, r) }))
		defer server.Close()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		peer.conn = conn
		peerErr = script(peer)
		_ = conn.CloseNow()
		select {
		case <-finished:
		case <-ctx.Done():
			t.Fatal("inbound handler did not release its mode")
		}
		select {
		case <-ctx.Done():
			t.Fatal("inbound handler did not stop")
		case event := <-client.Events():
			for event.Kind != "disconnected" {
				select {
				case event = <-client.Events():
				case <-ctx.Done():
					t.Fatal("inbound handler did not report disconnection")
				}
			}
			clientErr = event.Err
		}
	}
	if peerErr != nil {
		t.Fatal("scripted peer:", peerErr)
	}
	if clientErr == nil || ctx.Err() != nil {
		t.Fatal("client did not terminate within the peer's independent deadline")
	}
	persisted, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if store.Saves.Load() != before || !reflect.DeepEqual(persisted, credentials) {
		t.Fatal("handshake failure changed persisted identity, trust or playback state")
	}
	client.mu.Lock()
	released := client.active == nil && len(client.sessions) == 0 && client.modeUsers == 0
	client.mu.Unlock()
	if !released {
		t.Fatal("failed handshake leaked admission/session/mode state")
	}
}

func TestHandshakeMalformedInitialPhaseMatrixClosesSilently(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"outbound", "inbound"} {
			for _, fault := range handshakeFaults() {
				t.Run(suite+"/"+direction+"/"+fault.name, func(t *testing.T) {
					runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
						init, err := p.readClientInit()
						if err != nil {
							return err
						}
						if fault.phase == "init" {
							typ := websocket.MessageText
							if fault.binary {
								typ = websocket.MessageBinary
							}
							if err = p.conn.Write(p.ctx, typ, p.expand(fault.body)); err != nil {
								return err
							}
							return p.closedWithoutData(false)
						}
						prologue, err := p.initialPrologue(init)
						if err != nil {
							return err
						}
						if fault.phase == "before-noise" || fault.phase == "noise" {
							typ := websocket.MessageText
							if fault.binary {
								typ = websocket.MessageBinary
							}
							if err = p.conn.Write(p.ctx, typ, p.expand(fault.body)); err != nil {
								return err
							}
						} else {
							_, raw, err := p.noise1(prologue, fault)
							if err != nil {
								return err
							}
							if err = p.writeText(raw); err != nil {
								return err
							}
						}
						return p.closedWithoutData(false)
					})
				})
			}
		}
	}
}

func TestRehandshakeMalformedPhaseMatrixClosesSilently(t *testing.T) {
	faults := []handshakeFault{}
	for _, fault := range handshakeFaults() {
		if fault.phase == "inner" || fault.phase == "noise" {
			faults = append(faults, fault)
		}
	}
	faults = append(faults, handshakeFault{name: "lookup-miss-no-sentinel-fallback", phase: "inner", body: `{"psk_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","psk_category":"lt"}`})
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"outbound", "inbound"} {
			for _, fault := range faults {
				t.Run(suite+"/"+direction+"/"+fault.name, func(t *testing.T) {
					runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
						if err := p.establish(); err != nil {
							return err
						}
						var raw []byte
						if fault.phase == "noise" {
							raw = p.expand(fault.body)
						} else {
							_, message, err := p.noise1(p.hash, fault)
							if err != nil {
								return err
							}
							raw = message
						}
						if err := p.sendSecure(raw); err != nil {
							return err
						}
						return p.closedWithoutData(true)
					})
				})
			}
		}
	}
}
func (p *handshakePeer) establish() error {
	if err := p.initialTransport(); err != nil {
		return err
	}
	return p.activate(true)
}

func (p *handshakePeer) initialTransport() error {
	init, err := p.readClientInit()
	if err != nil {
		return err
	}
	prologue, err := p.initialPrologue(init)
	if err != nil {
		return err
	}
	hs, raw, err := p.noise1(prologue, handshakeFault{})
	if err != nil {
		return err
	}
	if err = p.writeText(raw); err != nil {
		return err
	}
	if err = p.noise2(hs, false); err != nil {
		return err
	}
	return nil
}

func TestHandshakeUnknownFieldsAndRekeyPositiveControl(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"outbound", "inbound"} {
			t.Run(suite+"/"+direction, func(t *testing.T) {
				runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
					if err := p.establish(); err != nil {
						return err
					}
					hs, raw, err := p.noise1(p.hash, handshakeFault{})
					if err != nil {
						return err
					}
					if err = p.sendSecure(raw); err != nil {
						return err
					}
					if err = p.noise2(hs, true); err != nil {
						return err
					}
					if err = p.activate(false); err != nil {
						return err
					}
					// Explicit peer shutdown, only after a fresh-key clock request proves
					// that both handshakes/activation and literal message-2 checks passed.
					return p.conn.CloseNow()
				})
			})
		}
	}
}

func TestHandshakeFullSizeNoiseAndTransportLimitBoundaries(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"outbound", "inbound"} {
			t.Run(suite+"/"+direction, func(t *testing.T) {
				runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
					init, err := p.readClientInit()
					if err != nil {
						return err
					}
					prologue, err := p.initialPrologue(init)
					if err != nil {
						return err
					}
					// KK message 1: 32-byte ephemeral key + encrypted payload + 16-byte
					// tag. Its raw Noise bytes are valid at the full 65535-byte limit;
					// the cleartext JSON/base64 carrier is necessarily larger.
					base := `{"psk_id":"$PSK","psk_category":"lt","padding":""}`
					body := strings.Replace(base, `"padding":""`, `"padding":"`+strings.Repeat("x", 65535-48-len(p.expand(base)))+`"`, 1)
					hs, raw, err := p.noise1(prologue, handshakeFault{body: body})
					if err != nil {
						return err
					}
					_, payload, err := peerMessage(raw)
					if err != nil {
						return err
					}
					var carrier struct {
						Data string `json:"data"`
					}
					if err = json.Unmarshal(payload, &carrier); err != nil {
						return err
					}
					data, err := base64.RawURLEncoding.Strict().DecodeString(carrier.Data)
					if err != nil || len(data) != 65535 {
						return errors.New("test did not generate exactly 65535 Noise bytes")
					}
					if err = p.writeText(raw); err != nil {
						return err
					}
					if err = p.noise2(hs, false); err != nil {
						return err
					}
					if err = p.activate(true); err != nil {
						return err
					}
					hs, raw, err = p.noise1(p.hash, handshakeFault{body: body})
					if err != nil {
						return err
					}
					if err = p.sendSecure(raw); err != nil {
						return err
					}
					if err = p.noise2(hs, true); err != nil {
						return err
					}
					if err = p.activate(false); err != nil {
						return err
					}
					// A valid AEAD packet, but one byte beyond the Noise transport
					// maximum, must still be rejected after both full-size handshakes.
					packet, err := p.out.Encrypt(nil, nil, append([]byte{240}, make([]byte, 65519)...))
					if err != nil {
						return err
					}
					if len(packet) != 65536 {
						return errors.New("test did not exceed the transport limit by exactly one byte")
					}
					if err = p.conn.Write(p.ctx, websocket.MessageBinary, packet); err != nil {
						return err
					}
					return p.closedWithoutData(false)
				})
			})
		}
	}
}

func TestHandshakeActivationBarriersRejectPrematureApplicationData(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"outbound", "inbound"} {
			for _, phase := range []string{"before-hello", "before-initial-activate", "after-rekey"} {
				for _, kind := range []string{"future/message", "group/update", "server/hello", "unknown-binary", "cleartext", "old-key"} {
					if phase == "before-hello" && kind == "server/hello" || phase != "after-rekey" && kind == "old-key" {
						continue
					}
					t.Run(suite+"/"+direction+"/"+phase+"/"+kind, func(t *testing.T) {
						runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
							var oldOut *noise.CipherState
							if phase == "after-rekey" {
								if err := p.establish(); err != nil {
									return err
								}
								hs, raw, err := p.noise1(p.hash, handshakeFault{})
								if err != nil {
									return err
								}
								if err = p.sendSecure(raw); err != nil {
									return err
								}
								oldOut = p.out
								if err = p.noise2(hs, true); err != nil {
									return err
								}
							} else {
								if err := p.initialTransport(); err != nil {
									return err
								}
								if phase == "before-initial-activate" {
									if err := p.sendSecure(peerJSON("server/hello", map[string]any{"name": "Barrier probe"})); err != nil {
										return err
									}
									name, _, err := p.readSecure()
									if err != nil {
										return err
									}
									if name != "client/hello" {
										return errors.New("missing pre-activation client hello")
									}
								}
							}
							payload := map[string]any{}
							if kind == "group/update" {
								payload = map[string]any{"group_id": "g", "group_name": "g", "playback_state": "stopped"}
							}
							if kind == "server/hello" {
								payload = map[string]any{"name": "Duplicate hello"}
							}
							raw := peerJSON(kind, payload)
							var err error
							switch kind {
							case "unknown-binary":
								var packet []byte
								packet, err = p.out.Encrypt(nil, nil, []byte{240, 0, 1})
								if err == nil {
									err = p.conn.Write(p.ctx, websocket.MessageBinary, packet)
								}
							case "cleartext":
								err = p.writeText(peerJSON("server/activate", map[string]any{"activities": []string{}}))
							case "old-key":
								var packet []byte
								packet, err = oldOut.Encrypt(nil, nil, append([]byte{0}, peerJSON("server/activate", map[string]any{"activities": []string{}})...))
								if err == nil {
									err = p.conn.Write(p.ctx, websocket.MessageBinary, packet)
								}
							default:
								err = p.sendSecure(raw)
							}
							if err != nil {
								return err
							}
							return p.closedWithoutData(false)
						})
					})
				}
			}
		}
	}
}

func TestHandshakePhaseTimeoutsDoNotSendApplicationMessages(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"outbound", "inbound"} {
			for _, phase := range []string{"server-init", "noise-1", "initial-activate", "rekey-activate"} {
				t.Run(suite+"/"+direction+"/"+phase, func(t *testing.T) {
					runHandshakePeer(t, suite, direction, 500*time.Millisecond, func(p *handshakePeer) error {
						switch phase {
						case "server-init":
							if _, err := p.readClientInit(); err != nil {
								return err
							}
						case "noise-1":
							init, err := p.readClientInit()
							if err != nil {
								return err
							}
							if _, err = p.initialPrologue(init); err != nil {
								return err
							}
						case "initial-activate":
							if err := p.initialTransport(); err != nil {
								return err
							}
							if err := p.sendSecure(peerJSON("server/hello", map[string]any{"name": "Activation timeout"})); err != nil {
								return err
							}
							name, _, err := p.readSecure()
							if err != nil {
								return err
							}
							if name != "client/hello" {
								return errors.New("missing client hello before timeout")
							}
						case "rekey-activate":
							if err := p.establish(); err != nil {
								return err
							}
							hs, raw, err := p.noise1(p.hash, handshakeFault{})
							if err != nil {
								return err
							}
							if err = p.sendSecure(raw); err != nil {
								return err
							}
							if err = p.noise2(hs, true); err != nil {
								return err
							}
						}
						return p.closedWithoutData(false)
					})
				})
			}
		}
	}
}

func TestServerInitErrorHintsCloseWithoutCredentialMutation(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, direction := range []string{"outbound", "inbound"} {
			for _, reason := range []string{`"unsupported_version"`, `"unsupported_suite"`, `"malformed"`, `null`, `false`, `"future"`} {
				t.Run(suite+"/"+direction+"/"+reason, func(t *testing.T) {
					runHandshakePeer(t, suite, direction, 2*time.Second, func(p *handshakePeer) error {
						if _, err := p.readClientInit(); err != nil {
							return err
						}
						if err := p.writeText([]byte(`{"type":"server/error","payload":{"reason":` + reason + `}}`)); err != nil {
							return err
						}
						return p.closedWithoutData(false)
					})
				})
			}
		}
	}
}
