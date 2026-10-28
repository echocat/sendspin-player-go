package wire

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/flynn/noise"
)

func TestWireBase64URLIsCanonicalNotOperatorInput(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 31, 32, 33, 127} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i * 173)
		}
		canonical := base64.RawURLEncoding.EncodeToString(data)
		decoded, err := decodeBase64URL(canonical)
		if err != nil || !bytes.Equal(decoded, data) {
			t.Fatal("rejected canonical encoding", size, err)
		}
		for _, bad := range []string{"\n" + canonical, canonical + "\r", canonical[:len(canonical)/2] + "\r\n" + canonical[len(canonical)/2:], canonical + "="} {
			if decoded, err = decodeBase64URL(bad); !errors.Is(err, protocol.ErrProtocol) || decoded != nil {
				t.Fatal("accepted noncanonical wire encoding", size, err)
			}
		}
	}
	for _, bad := range []string{"AB", "AAB", "_x", "//8", "AA AA", "AA\tAA"} {
		if _, err := decodeBase64URL(bad); !errors.Is(err, protocol.ErrProtocol) {
			t.Fatal("accepted invalid alphabet or unused bits", err)
		}
	}
}

func FuzzWireBase64URL(f *testing.F) {
	for _, seed := range []string{"", "AA", "AAB", "AB", "AA\nAA", "_w", "AA=", "α"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 128<<10 {
			return
		}
		decoded, err := decodeBase64URL(value)
		if err != nil {
			if decoded != nil {
				t.Fatal("failed decoding returned partial data")
			}
			return
		}
		if base64.RawURLEncoding.EncodeToString(decoded) != value {
			t.Fatal("accepted a noncanonical wire value")
		}
	})
}

func TestNoncanonicalServerIdentityFailsBeforeResolver(t *testing.T) {
	key, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	canonical := base64.RawURLEncoding.EncodeToString(key.Public)
	for _, separator := range []string{"\n", "\r", "\r\n"} {
		t.Run(base64.RawURLEncoding.EncodeToString([]byte(separator)), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			bad := canonical[:8] + separator + canonical[8:]
			done := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if _, _, err = conn.Read(ctx); err != nil {
					done <- err
					return
				}
				message, _ := protocol.Encode("server/init", map[string]any{"server_id": bad, "version": 1})
				done <- conn.Write(ctx, websocket.MessageText, message)
			}))
			defer server.Close()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.CloseNow() }()
			resolved := false
			_, err = Handshake(ctx, conn, key, ChaCha, 65535, func(string) Resolve { resolved = true; return nil }, nil)
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			if !errors.Is(err, protocol.ErrProtocol) || resolved {
				t.Fatal("noncanonical identity reached credential selection", err, resolved)
			}
		})
	}
}
