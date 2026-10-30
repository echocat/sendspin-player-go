# Optional PIN pairing (v0.2.0)

Both RC1 PIN procedures are supported in addition to mandatory PSK/token pairing:
**dynamic six-digit codes** and **static eight-digit per-device codes**. They
are opt-in APIs in SDK **v0.2.0**, not part of v0.1.1. PSK-only pairing remains
the unchanged default when `CodePairing` is nil.

RC1 permits **at most one code method in a client's hello**. The SDK implements
both, but each Client selects either Static or Display; configuring both is an
error before credentials are written. PSK/token pairing remains offered in both
modes. A usable display should normally select dynamic pairing.

## Dynamic display PIN

```go
CodePairing: &sendspin.CodePairingConfig{
    Display: func(ctx context.Context, event sendspin.PairingDisplay) error {
        // Post to your UI, return promptly, and honor ctx.
        // Active: render event.PIN.Grouped(); inactive: hide the pairing UI.
        // Do not call Client methods from this actor callback.
        return nil
    },
},
```

The application takes over its display while Active, suspending artwork or any
other role output on that same display without changing availability or stream
timelines. It restores presentation on Active=false. This is not a diagnostic
or logging hook. This SDK does not automatically speak PINs, open capture, change
volume or render a GUI. Digits/display is the advertised format/channel; dynamic
QR and speaker emission are not advertised or claimed.

The code is bound to the exact current Noise handshake, server nonce and the
client's committed random nonce. Retries keep the same displayed code but use
fresh CPace scalars and distinct round-bound session IDs. Twenty emitted rounds
without successful server key confirmation hold further attempts behind a
deliberate local operator action. This device-global count is persisted and is
not partitioned by server identity/address or reset by reopening the SDK.

## Static device PIN

```go
CodePairing: &sendspin.CodePairingConfig{Static: true},
```

New provisions a uniformly random eight-digit code per device and persists it
with the private credentials. Existing identities are preserved. Reboots do not
change the code; there is no shared factory default.

An intentional local provisioning UI can call `client.StaticPairingPIN()` and
present `pin.Grouped()` to the operator. The server is where the operator enters
the code. Never include Value/Grouped in routine logs or telemetry.

Before accepting a static attempt, the application must handle a deliberate
**local** gesture and call `client.OpenPairingWindow(ctx)`. Do not expose this
method through unauthenticated network commands. It opens a five-minute window,
binds it to the connection carrying the first admitted attempt, and closes it
on success, five failed confirmations, connection drop, local cancellation or
expiry. An already-started attempt retains its own two-minute deadline when
the window expires. A pending attempt emits `client/pair-pending`, not pair-init.

The same local action resets the dynamic twenty-round lockout. It is rejected
while an attempt is active; `client.CancelPairing(ctx)` aborts one and closes the
local window. The secret-free `pairing_pending` event can prompt a local UI.

## Authentication and storage

The implementation follows Sendspin's pinned RC1 profile and
`draft-irtf-cfrg-cpace-21`, **CPACE-X25519-SHA512**, initiator/responder MCF:

- SHA-512, HMAC, X25519 and randomness use Go's standard library. Constant-time
  field arithmetic for Elligator2 uses `filippo.io/edwards25519/field` v1.2.0;
  there is no variable-time `math/big` password/group implementation.
- Noise hash, activation index and round are bound exactly as RC1 specifies.
  Wrong/low-order shares and malformed encodings close without credential reveal.
- No long-term PSK is exposed before the server's confirmation verifies.
  The new PSK and dynamic nonce opening are separately AEAD-wrapped with the
  prescribed labels, session ID, ISK and negotiated cipher suite.
- The new pairing record is committed only after server/pair-finalize. Abort,
  timeout, superseding activation and failed Store transactions cannot commit it.
- Formatting and JSON redact PairingPIN/PairingDisplay secrets. Value and Grouped
  are intentional operator disclosure. The credential Store serialization, like
  existing PSKs, necessarily contains secret material and must stay protected.
  Static-PIN pointers are detached at Store boundaries.

Attempts are bounded by two minutes from pair-init, not pair-pending, including
Store/UI work. Callbacks/Stores must honor their contexts; the SDK cannot forcibly
preempt custom code. Go garbage collection prevents a guarantee that every secret
copy is erased, although PAKE state is consumed and cleared on termination.
Counter transactions carry that same attempt deadline and are checked again
after returning, so even a late-returning Store cannot trigger new PIN display
or credential output. Actual client/pair-* transmissions use the earlier of
the attempt deadline, parent cancellation and the normal five-second write
budget; pending/abort notifications do not inherit an expired attempt budget.
Peer cancellation closes only its bound static window, not an unbound local
gesture window or another connection's window. The local CancelPairing action
still deliberately closes the device's local window.

## Evidence and limits

Offline tests replay the independent IETF draft-21 X25519 generator/share/ISK
vector. Managed E2E uses the **unmodified aiosendspin 10.2.1 and cpace 0.1.0**
protocol/crypto implementation, with operator code entry controlled over private
loopback. Both PIN methods and both cipher suites interoperate, including dynamic
retry, the persistent twenty-round limit, the static five-failure window and
local unlocking. PINs and raw PAKE bodies are not retained in fixture events.
An additional 172-case frozen differential corpus independently compares
generators, shared keys, ISKs, MCF tags and acceptance decisions. The verifier
checks the pinned oracle source and refuses to silently regenerate fixtures.

These tests are **not an independent cryptographic security review**. They do
not certify physical display behavior, human gestures, side-channel resilience
on arbitrary devices, printed provisioning labels or product usability.

The frozen primary conformance inventory continues to describe the default
PSK-only profile (148 historical groups/742 primary atoms). Optional PIN E2Es
have explicit extension dispositions, not inherited fulfillment of formerly
excluded code clauses. A complete optional-profile source inventory/product and
independent security review remain required before any PIN conformance/security
certification claim. No historical IDs/assessments or published tags are removed.

See [internal PIN hardening review](pin-pairing-hardening.md) for reproduced
deadline/window defects, failure-path tests and the limits of that review.
