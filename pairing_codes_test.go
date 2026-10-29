package sendspin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStaticPINProvisioningPersistenceAndRedaction(t *testing.T) {
	store := &MemoryStore{}
	cfg := Config{Name: "PIN device", Store: store, Roles: []Role{}, CodePairing: &CodePairingConfig{Static: true}}
	c, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := c.StaticPairingPIN()
	if !ok || len(pin.Value()) != 8 || len(pin.Grouped()) != 9 {
		t.Fatal("missing static PIN")
	}
	for _, r := range pin.Value() {
		if r < '0' || r > '9' {
			t.Fatal("nondecimal PIN")
		}
	}
	again, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := again.StaticPairingPIN()
	if pin.Value() != other.Value() {
		t.Fatal("PIN changed on reboot")
	}
	for _, value := range []any{pin, PairingDisplay{Active: true, PIN: pin}, c.credentials} {
		if strings.Contains(fmt.Sprintf("%+v %#v %s", value, value, value), pin.Value()) {
			t.Fatal("PIN leaked through formatting")
		}
	}
	b, _ := json.Marshal(PairingDisplay{Active: true, PIN: pin})
	if strings.Contains(string(b), pin.Value()) {
		t.Fatal("PIN leaked through display telemetry JSON")
	}
	loaded, _ := store.Load(context.Background())
	saved := *loaded.StaticPairingPIN
	loaded.StaticPairingPIN[0] = 'x'
	loaded, _ = store.Load(context.Background())
	if *loaded.StaticPairingPIN != saved {
		t.Fatal("Store exposed mutable PIN")
	}
	if err = c.OpenPairingWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Until(c.pairWindow.expires) < 4*time.Minute {
		t.Fatal("window missing")
	}
	if err = c.CancelPairing(context.Background()); err != nil || !c.pairWindow.expires.IsZero() {
		t.Fatal(err)
	}
}
func TestPINMethodsRemainOptInAndRoundLimitPersists(t *testing.T) {
	store := &MemoryStore{}
	c, err := New(context.Background(), Config{Name: "Default", Store: store, Roles: []Role{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.StaticPairingPIN(); ok {
		t.Fatal("PIN enabled by default")
	}
	cred, _ := store.Load(context.Background())
	cred.DynamicPairingRounds = 20
	if err = store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	c, err = New(context.Background(), Config{Name: "Limited", Store: store, Roles: []Role{}, CodePairing: &CodePairingConfig{Display: func(context.Context, PairingDisplay) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	if c.credentials.DynamicPairingRounds != 20 {
		t.Fatal("reboot bypassed global limit")
	}
	if err = c.OpenPairingWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	cred, _ = store.Load(context.Background())
	if cred.DynamicPairingRounds != 0 {
		t.Fatal("local gesture did not reset durable count")
	}
}

func TestCodePairingConfigurationOffersOnlyOneCodeMethod(t *testing.T) {
	store := &MemoryStore{}
	_, err := New(context.Background(), Config{Name: "Invalid both", Roles: []Role{}, Store: store, CodePairing: &CodePairingConfig{Static: true, Display: func(context.Context, PairingDisplay) error { return nil }}})
	if err == nil {
		t.Fatal("both code methods offered in one hello")
	}
	if _, err = store.Load(context.Background()); err != ErrNoCredentials {
		t.Fatal("invalid configuration wrote identity")
	}
}
