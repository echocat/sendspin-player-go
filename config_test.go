package sendspin

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestUnsupportedProfileHasNoStorageSideEffects(t *testing.T) {
	store := &MemoryStore{}
	if _, err := New(context.Background(), Config{Name: "test", Store: store, Roles: []Role{Controller}, Specification: "2.0.0"}); err == nil {
		t.Fatal("accepted unimplemented profile")
	}
	if _, err := store.Load(context.Background()); err != ErrNoCredentials {
		t.Fatal("unsupported profile created credentials", err)
	}
}
func TestEverySecretFormatIsRedacted(t *testing.T) {
	secret := Secret{1, 2, 3}
	for _, format := range []string{"%s", "%v", "%#v", "%d", "%x", "%q", "%+v"} {
		if text := fmt.Sprintf(format, secret); !strings.Contains(text, "REDACTED") {
			t.Fatalf("format %s leaked secret: %s", format, text)
		}
	}
}

func TestUnsupportedRolesHaveNoStorageSideEffects(t *testing.T) {
	for _, role := range []Role{"source@v1", "_custom@v1", "player@v2", "controller@_experimental"} {
		store := &MemoryStore{}
		if _, err := New(context.Background(), Config{Name: "Unsupported role", Store: store, Roles: []Role{role}}); err == nil {
			t.Fatal("accepted unimplemented role/version", role)
		}
		if _, err := store.Load(context.Background()); err != ErrNoCredentials {
			t.Fatal("unsupported role touched credentials", role, err)
		}
	}
}

func TestDefaultAudioConfigurationOffersPlayerButExplicitRolesRemainLocal(t *testing.T) {
	client, err := New(context.Background(), Config{Name: "Default audio roles", Store: &MemoryStore{}, Output: unusedOutput{}})
	if err != nil {
		t.Fatal(err)
	}
	player := false
	for _, role := range client.cfg.Roles {
		player = player || role == Player
	}
	if !player {
		t.Fatal("audio-capable default configuration does not offer player")
	}
	if _, err = New(context.Background(), Config{Name: "Missing output", Store: &MemoryStore{}}); err == nil {
		t.Fatal("default player was silently offered without output")
	}
	client, err = New(context.Background(), Config{Name: "Explicit controller", Store: &MemoryStore{}, Output: unusedOutput{}, Roles: []Role{Controller}})
	if err != nil || len(client.cfg.Roles) != 1 || client.cfg.Roles[0] != Controller {
		t.Fatal("explicit local role choice was overridden", err)
	}
}
