//go:build e2e

package sendspin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/wire"
)

func TestE2EReferenceAutomaticPINPairingAndRestart(t *testing.T) {
	for _, suite := range []string{wire.ChaCha, wire.AES} {
		for _, inbound := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inbound=%t", suite, inbound), func(t *testing.T) {
				c, ref, store, shown := codePairClient(t, suite, "automatic", inbound)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				old, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !c.PairingState().Ready {
					t.Fatal("unpaired startup requires operator gesture")
				}
				done := pairCodeAsync(ctx, ref, "dynamic_pairing_code")
				select {
				case callback := <-shown:
					pin, ok := c.CurrentPairingPIN()
					if !ok || pin.PIN.Value() != callback.Value() || len(pin.PIN.Value()) != 6 {
						t.Fatal("callback and explicit PIN display disagree")
					}
					submitPIN(t, ctx, ref, pin.PIN.Value())
				case <-ctx.Done():
					t.Fatal("no automatic PIN", ctx.Err())
				}
				if err := waitCodePairResult(t, ctx, done); err != nil {
					t.Fatal(err)
				}
				eventually(t, func() bool {
					c.mu.Lock()
					defer c.mu.Unlock()
					return c.active != nil && c.active.recordServerID == ref.id
				})
				if _, ok := c.CurrentPairingPIN(); ok || c.PairingState().Ready || !c.PairingState().Paired {
					t.Fatal("durable pairing still offers PIN readiness")
				}
				before, err := store.Load(ctx)
				if err != nil || len(before.Records) != 1 || before.DynamicPairingRounds != 0 || before.PairingAuthPending {
					t.Fatal("actual PAKE confirmation did not persist trust/protection", err)
				}
				result, err := c.RestartPairing(ctx)
				if err != nil || !result.Committed {
					t.Fatal("paired session reset failed", err)
				}
				if p := c.PairingState(); !p.Ready || p.Paired || p.Restarting {
					t.Fatal("reset left an old session or no readiness", p)
				}
				after, err := store.Load(ctx)
				if err != nil || len(after.Records) != 0 || after.PrivateKey != old.PrivateKey || after.PairingPSK == old.PairingPSK || after.Volume != before.Volume || after.Muted != before.Muted || after.OutputDelayMS != before.OutputDelayMS {
					t.Fatal("reset did not preserve settings/identity and revoke credentials", err)
				}
				c.mu.Lock()
				remaining := len(c.sessions)
				c.mu.Unlock()
				if remaining != 0 {
					t.Fatal("successful reset retained old authenticated transports")
				}
			})
		}
	}
}

func TestE2EReferenceAutomaticPINFiveFailuresAndPendingCooldown(t *testing.T) {
	c, ref, store, shown := codePairClient(t, wire.ChaCha, "automatic")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := pairCodeAsync(ctx, ref, "dynamic_pairing_code")
	for range 5 {
		select {
		case pin := <-shown:
			submitPIN(t, ctx, ref, wrongPIN(pin))
		case <-ctx.Done():
			t.Fatal("no retry display", ctx.Err())
		}
	}
	if err := waitCodePairResult(t, ctx, done); err == nil {
		t.Fatal("wrong code paired")
	}
	if p := c.PairingState(); p.Ready || p.CooldownRemaining < 29*time.Second || p.Rounds != 5 {
		t.Fatal("reference-peer failures did not enforce cooldown", p)
	}
	if _, ok := c.CurrentPairingPIN(); ok {
		t.Fatal("aborted code still visible")
	}
	done = pairCodeAsync(ctx, ref, "dynamic_pairing_code")
	eventually(t, func() bool { return referenceEventCount(t, ref, "code_pending", "") > 0 })
	select {
	case <-shown:
		t.Fatal("pending attempt bypassed cooldown")
	default:
	}
	// Advance the private monotonic test deadline; the persistent flag stays
	// set until the first new round commits. No wall-clock or sleep dependency.
	c.mu.Lock()
	c.pairingCooldownUntil = pairingNow().Add(-time.Second)
	c.mu.Unlock()
	select {
	case pin := <-shown:
		submitPIN(t, ctx, ref, pin.Value())
	case <-ctx.Done():
		t.Fatal("cooldown did not resume pending attempt", ctx.Err())
	}
	if err := waitCodePairResult(t, ctx, done); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		stored, err := store.Load(ctx)
		return err == nil && len(stored.Records) == 1 && stored.PairingFailures == 0 && !stored.PairingCooldown && !stored.PairingAuthPending
	})
}

func TestE2EReferenceAutomaticPINRestartAbortsInProgressAttempt(t *testing.T) {
	c, ref, store, shown := codePairClient(t, wire.ChaCha, "automatic")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := pairCodeAsync(ctx, ref, "dynamic_pairing_code")
	select {
	case <-shown:
	case <-ctx.Done():
		t.Fatal("no initial PIN", ctx.Err())
	}
	result, err := c.RestartPairing(ctx)
	if err != nil || !result.Committed {
		t.Fatal("in-progress reset failed", err)
	}
	if _, ok := c.CurrentPairingPIN(); ok {
		t.Fatal("reset retained old PIN display")
	}
	if err := waitCodePairResult(t, ctx, done); err == nil {
		t.Fatal("old pairing completed after restart")
	}
	stored, err := store.Load(ctx)
	if err != nil || len(stored.Records) != 0 || stored.DynamicPairingRounds != 0 {
		t.Fatal("old pairing state reappeared after reset", err)
	}
	if p := c.PairingState(); !p.Ready || p.Active || p.Restarting {
		t.Fatal("reset did not release automatic readiness", p)
	}
}
