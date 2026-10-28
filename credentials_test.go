package sendspin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPairingTokenVector(t *testing.T) {
	const vector = "SP:0AAAQEAYEAUDAOCAJBIFQYDIOB4IBCEQTCQKRMFYYDENBWHA5DYP6BYPC4PSOLZXH5DU6V97M5XXO74HR6LZ7J5PW674PT6X37T6757Y"
	id, psk, err := ParsePairingToken("  " + strings.ToLower(vector) + "  ")
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := base64.RawURLEncoding.DecodeString(id)
	for i := range 32 {
		if pub[i] != byte(i) || psk[i] != byte(224+i) {
			t.Fatal("vector mismatch")
		}
	}
	for _, bad := range []string{"", "SP:1ABC", "SP:0!!!!", "SP:0ABC"} {
		if _, _, err := ParsePairingToken(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
func TestCredentialsPersistenceAndRedaction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "device.json")
	store := FileStore{Path: path}
	c, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx)
	if err != nil || got.ClientID() != c.ClientID() || got.PairingPSK != c.PairingPSK {
		t.Fatal("roundtrip", err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), base64.RawURLEncoding.EncodeToString(c.PrivateKey[:])) {
		t.Fatal("secret serialization is not explicit base64url")
	}
	for _, text := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprint(c.PrivateKey)} {
		if !strings.Contains(text, "REDACTED") {
			t.Fatal("not redacted", text)
		}
	}
	id, psk, err := ParsePairingToken(c.PairingToken())
	if err != nil || id != c.ClientID() || psk != c.PairingPSK {
		t.Fatal("token roundtrip", err)
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Load(ctx); err == nil {
			t.Fatal("accepted public credentials")
		}
	}
}
func TestResolverBindingAndSentinelFallback(t *testing.T) {
	ctx := context.Background()
	store := &MemoryStore{}
	c, err := New(ctx, Config{Name: "test", Store: store, Roles: []Role{Controller}})
	if err != nil {
		t.Fatal(err)
	}
	server, _ := NewCredentials()
	other, _ := NewCredentials()
	secret := Secret{1}
	c.credentials.Records = []PairingRecord{{ServerID: server.ClientID(), PSK: secret}}
	if _, err = c.resolver(other.ClientID())(pskID(secret), "lt", true); err == nil {
		t.Fatal("accepted misbound key")
	}
	if got, err := c.resolver(server.ClientID())("missing", "lt", true); err != nil || got.Category != "sn" {
		t.Fatal("initial fallback", err)
	}
	if _, err = c.resolver(server.ClientID())("missing", "lt", false); err == nil {
		t.Fatal("rehandshake fell back")
	}
}

func TestLastPlaybackServerSurvivesClientRestart(t *testing.T) {
	ctx := context.Background()
	store := FileStore{Path: filepath.Join(t.TempDir(), "device.json")}
	config := Config{Name: "Last playback", Store: store, Roles: []Role{Controller}}
	first, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err = first.update(ctx, func(c *Credentials) error { c.LastPlaybackServer = server.ClientID(); return nil }); err != nil {
		t.Fatal(err)
	}
	second, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if second.credentials.LastPlaybackServer != server.ClientID() || second.ClientID() != first.ClientID() {
		t.Fatal("restart lost server preference or changed device identity")
	}
}
