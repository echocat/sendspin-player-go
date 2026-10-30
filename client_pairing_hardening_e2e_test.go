//go:build e2e

package sendspin

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

var errPINStoreRefusal = errors.New("controlled PIN persistence failure")

type pinRefusingStore struct {
	MemoryStore
	armed atomic.Bool
	stage string
}

func (s *pinRefusingStore) Save(ctx context.Context, c Credentials) error {
	if s.armed.Load() && ((s.stage == "emit" && c.DynamicPairingRounds > 0) || (s.stage == "confirm" && c.DynamicPairingRounds == 0)) {
		return errPINStoreRefusal
	}
	return s.MemoryStore.Save(ctx, c)
}

func TestE2EReferencePINCounterStoreFailuresDoNotRevealCredentials(t *testing.T) {
	for _, stage := range []string{"emit", "confirm"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			store := &pinRefusingStore{stage: stage}
			shown := make(chan PairingPIN, 2)
			var hides atomic.Int32
			c, err := New(ctx, Config{Name: "PIN failed Store", Store: store, Roles: []Role{Controller}, CodePairing: &CodePairingConfig{Display: func(_ context.Context, e PairingDisplay) error {
				if e.Active {
					shown <- e.PIN
				} else {
					hides.Add(1)
				}
				return nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			cred, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ref := startReferenceWithOptions(t, cred, sentinelPSK, false, map[string]any{"no_record": true, "observe_pairing": true})
			connection := connectReference(t, c, ref)
			eventually(t, func() bool {
				return ref.control(t, map[string]any{"command": "status"})["connected"] == true && c.Snapshot().ServerID == ref.id
			})
			store.armed.Store(true)
			done := pairCodeAsync(ctx, ref, "dynamic_pairing_code")
			if stage == "confirm" {
				select {
				case pin := <-shown:
					submitPIN(t, ctx, ref, pin.Value())
				case <-ctx.Done():
					t.Fatal("no emitted PIN")
				}
			}
			select {
			case <-connection.done:
				if !errors.Is(connection.err, errPINStoreRefusal) {
					t.Fatal("Store failure identity lost", connection.err)
				}
			case <-ctx.Done():
				t.Fatal("Store refusal did not close pairing")
			}
			if err := waitCodePairResult(t, ctx, done); err == nil {
				t.Fatal("reference paired after client Store refusal")
			}
			stored, err := store.Load(ctx)
			if err != nil || len(stored.Records) != 0 {
				t.Fatal("failed transaction persisted trust", err)
			}
			if referenceEventCount(t, ref, "pair_confirm", "") != 0 || referenceEventCount(t, ref, "pair_finalize", "") != 0 {
				t.Fatal("Store refusal disclosed confirmation or wrapped credentials")
			}
			if ref.control(t, map[string]any{"command": "status"})["pairing_record"] != false {
				t.Fatal("reference persisted refused pairing")
			}
			if stage == "emit" {
				select {
				case <-shown:
					t.Fatal("PIN displayed before counter persisted")
				default:
				}
			} else if hides.Load() != 1 {
				t.Fatal("abandoned display was not hidden exactly once", hides.Load())
			}
		})
	}
}

func TestE2EReferencePendingPINCancelDoesNotStartAttempt(t *testing.T) {
	c, ref, store, _ := codePairClient(t, "25519_ChaChaPoly_SHA256", "static_pairing_code")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := pairCodeAsync(ctx, ref, "static_pairing_code")
	eventually(t, func() bool { return referenceEventCount(t, ref, "code_pending", "") > 0 })
	if err := c.CancelPairing(ctx); err != nil {
		t.Fatal(err)
	}
	if err := waitCodePairResult(t, ctx, done); err == nil {
		t.Fatal("canceled pending PIN succeeded")
	}
	stored, err := store.Load(ctx)
	if err != nil || len(stored.Records) != 0 {
		t.Fatal("pending cancellation changed trust", err)
	}
	if referenceEventCount(t, ref, "pair_init", "") != 0 || referenceEventCount(t, ref, "pair_finalize", "") != 0 {
		t.Fatal("pending cancellation started an attempt")
	}
}
