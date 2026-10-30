//go:build e2e

package sendspin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/wire"
)

func codeControl(ctx context.Context, ref referenceServer, payload map[string]any) (map[string]any, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ref.url+"/control", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("private operator control failed")
	}
	var out map[string]any
	err = json.NewDecoder(response.Body).Decode(&out)
	return out, err
}
func codePairClient(t *testing.T, suite, method string, inbound ...bool) (*Client, referenceServer, *MemoryStore, <-chan PairingPIN) {
	t.Helper()
	ctx := context.Background()
	store := &MemoryStore{}
	shown := make(chan PairingPIN, 32)
	cfg := &CodePairingConfig{Static: method == "static_pairing_code"}
	if !cfg.Static {
		cfg.Display = func(_ context.Context, event PairingDisplay) error {
			if event.Active {
				shown <- event.PIN
			}
			return nil
		}
	}
	c, err := New(ctx, Config{Name: "PIN interop", Store: store, Roles: []Role{Controller}, CipherSuite: suite, CodePairing: cfg, DisableDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := store.Load(ctx)
	ref := startReferenceWithOptions(t, cred, sentinelPSK, false, map[string]any{"no_record": true, "observe_pairing": true})
	var connection *outboundTestConnection
	if len(inbound) > 0 && inbound[0] {
		endpoint, _ := serveTestClient(t, c)
		ref.control(t, map[string]any{"command": "connect", "url": endpoint, "pairing": false})
	} else {
		connection = connectReference(t, c, ref)
	}
	eventually(t, func() bool {
		if connection != nil {
			select {
			case <-connection.done:
				t.Fatalf("PIN fixture connection stopped: %v", connection.err)
			default:
			}
		}
		return ref.control(t, map[string]any{"command": "status"})["connected"] == true && c.Snapshot().ServerID == ref.id
	})
	return c, ref, store, shown
}
func pairCodeAsync(ctx context.Context, ref referenceServer, method string) <-chan error {
	done := make(chan error, 1)
	go func() {
		result, err := codeControl(ctx, ref, map[string]any{"command": "pair_code", "method": method})
		if err == nil && result["pair_abort"] != nil {
			err = fmt.Errorf("pairing aborted: %s", result["pair_abort"])
		}
		done <- err
	}()
	return done
}
func TestE2EReferenceStaticAndDynamicPINPairingBothCipherSuites(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, method := range []string{"static_pairing_code", "dynamic_pairing_code"} {
			for _, inbound := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/inbound=%t", suite, method, inbound), func(t *testing.T) {
					c, ref, store, shown := codePairClient(t, suite, method, inbound)
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if method == "static_pairing_code" {
						if err := c.OpenPairingWindow(ctx); err != nil {
							t.Fatal(err)
						}
						pin, _ := c.StaticPairingPIN()
						if _, err := codeControl(ctx, ref, map[string]any{"command": "submit_code", "code": pin.Value()}); err != nil {
							t.Fatal(err)
						}
					}
					done := pairCodeAsync(ctx, ref, method)
					if method == "dynamic_pairing_code" {
						select {
						case pin := <-shown:
							if len(pin.Value()) != 6 {
								t.Fatal("invalid dynamic length")
							}
							if _, err := codeControl(ctx, ref, map[string]any{"command": "submit_code", "code": pin.Value()}); err != nil {
								t.Fatal(err)
							}
						case <-ctx.Done():
							t.Fatal("no dynamic display", ctx.Err())
						}
					}
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					eventually(t, func() bool {
						c.mu.Lock()
						defer c.mu.Unlock()
						return c.active != nil && c.active.recordServerID == ref.id
					})
					cred, err := store.Load(ctx)
					if err != nil || len(cred.Records) != 1 || cred.Records[0].ServerID != ref.id || cred.Records[0].PSK == cred.PairingPSK {
						t.Fatal("missing new paired credential", err)
					}
					if ref.control(t, map[string]any{"command": "status"})["pairing_record"] != true {
						t.Fatal("independent server did not persist")
					}
				})
			}
		}
	}
}
func TestE2EReferenceDynamicPINRetryUsesSameCode(t *testing.T) {
	c, ref, store, shown := codePairClient(t, wire.ChaCha, "dynamic_pairing_code")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := pairCodeAsync(ctx, ref, "dynamic_pairing_code")
	var first PairingPIN
	for round := 0; round < 2; round++ {
		select {
		case pin := <-shown:
			if round == 0 {
				first = pin
			} else if pin.Value() != first.Value() {
				t.Fatal("retry changed the pairing code")
			}
			value := pin.Value()
			if round == 0 {
				value = string('0'+(value[0]-'0'+1)%10) + value[1:]
			}
			if _, err := codeControl(ctx, ref, map[string]any{"command": "submit_code", "code": value}); err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		cred, _ := store.Load(ctx)
		return len(cred.Records) == 1 && cred.DynamicPairingRounds == 0
	})
	_ = c
}

func wrongPIN(pin PairingPIN) string { v := pin.Value(); return string('0'+(v[0]-'0'+1)%10) + v[1:] }
func waitCodePairResult(t *testing.T, ctx context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("PIN fixture timeout")
		return ctx.Err()
	}
}
func submitPIN(t *testing.T, ctx context.Context, ref referenceServer, value string) {
	t.Helper()
	if _, err := codeControl(ctx, ref, map[string]any{"command": "submit_code", "code": value}); err != nil {
		t.Fatal(err)
	}
}

func TestE2EReferenceStaticPINWindowClosesAfterFiveFailures(t *testing.T) {
	c, ref, store, _ := codePairClient(t, wire.ChaCha, "static_pairing_code")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := c.OpenPairingWindow(ctx); err != nil {
		t.Fatal(err)
	}
	pin, _ := c.StaticPairingPIN()
	for range 5 {
		submitPIN(t, ctx, ref, wrongPIN(pin))
		if err := waitCodePairResult(t, ctx, pairCodeAsync(ctx, ref, "static_pairing_code")); err == nil {
			t.Fatal("wrong PIN authenticated")
		}
	}
	c.mu.Lock()
	closed := c.pairWindow.expires.IsZero()
	c.mu.Unlock()
	if !closed {
		t.Fatal("fifth failure did not close window")
	}
	cred, _ := store.Load(ctx)
	if len(cred.Records) != 0 {
		t.Fatal("unconfirmed PIN stored credentials")
	}
	done := pairCodeAsync(ctx, ref, "static_pairing_code")
	eventually(t, func() bool { return referenceEventCount(t, ref, "code_pending", "") > 0 })
	if err := c.OpenPairingWindow(ctx); err != nil {
		t.Fatal(err)
	}
	submitPIN(t, ctx, ref, pin.Value())
	if err := waitCodePairResult(t, ctx, done); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { cred, _ := store.Load(ctx); return len(cred.Records) == 1 })
}

func TestE2EReferenceDynamicPINGlobalTwentyRoundLimitAndLocalUnlock(t *testing.T) {
	c, ref, store, shown := codePairClient(t, wire.ChaCha, "dynamic_pairing_code")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := pairCodeAsync(ctx, ref, "dynamic_pairing_code")
	var first PairingPIN
	for round := 0; round < 20; round++ {
		select {
		case pin := <-shown:
			if round == 0 {
				first = pin
			} else if pin.Value() != first.Value() {
				t.Fatal("code changed within attempt")
			}
			submitPIN(t, ctx, ref, wrongPIN(pin))
		case <-ctx.Done():
			t.Fatal("missing emitted round")
		}
	}
	if err := waitCodePairResult(t, ctx, done); err == nil {
		t.Fatal("twentieth wrong PIN authenticated")
	}
	cred, _ := store.Load(ctx)
	if len(cred.Records) != 0 || cred.DynamicPairingRounds != 20 {
		t.Fatal("round limit not persisted")
	}
	done = pairCodeAsync(ctx, ref, "dynamic_pairing_code")
	eventually(t, func() bool { return referenceEventCount(t, ref, "code_pending", "") > 0 })
	select {
	case <-shown:
		t.Fatal("round limit bypassed without local gesture")
	default:
	}
	if err := c.OpenPairingWindow(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case pin := <-shown:
		submitPIN(t, ctx, ref, pin.Value())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := waitCodePairResult(t, ctx, done); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		cred, _ := store.Load(ctx)
		return len(cred.Records) == 1 && cred.DynamicPairingRounds == 0
	})
}
