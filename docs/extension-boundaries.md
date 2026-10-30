# Deliberately bounded extension points

These are integration contracts and review triggers, not newly implemented
features or permission to change the pinned RC1 wire behavior.

## Buffer calibration (P08.05 / .06)

Current `Config.RequiredLeadTime` and `Config.MinBuffer` are immutable, explicit
deployment inputs. They are not automatic measurements or guaranteed minimum
latencies. `Output`/`Device.Next` and the redacted transport observer separate
device timing from message arrival; neither makes an estimator optional under
the still-unresolved derivation wording.

A future estimator should be separate from rendering and accept a bounded window
of application-boundary arrival times, server timestamps, `send_ahead` and current
time-filter mapping/readiness. Reject both saturated `send_ahead` sentinel values
and preconvergence samples. Handle discontinuities/rekeys without mixing clock
epochs. Bound memory and computation; report sustained upper-tail changes only
after debouncing. Include intermittent interference, not just startup bursts.

Startup lead also includes codec/backend warmup and cannot be derived from the
arrival distribution alone. Never fold external `output_delay_ms` into either
hint. Validate static/deployment values empirically; no default is universal.
Any mutable timing API must serialize updates through the actor and preserve
current stream/format/buffer accounting. No such estimator/setter is added here.

## Code pairing (R01.08)

In the default profile only mandatory `pairing_psk` is advertised. Having an audio output or artwork
consumer is not automatically a usable operator out-channel. Optional static or
dynamic code pairing requires an explicit product-level choice and separate
protocol/crypto review; it is not implemented by adding a callback to print digits.

Any code-pairing interface must own capability advertisement, one offered code method,
display/speaker/QR format selection, local output interruption without moving
stream timelines, availability, cancellation, deliberate gestures, cooldowns,
attempt/round/window limits and operator language selection. Rendering a token
QR is distinct from CPace, commit/reveal, MCF verification and wrapped-secret
handling. The user-requested, opt-in [PIN extension](pin-pairing.md) now implements
both code protocols (one offered per Client), with an application-owned display.
It does not introduce speaker/QR output or a production-security certification;
the frozen baseline classifications are not promoted by its tests.

## Unpaired withdrawal and physical labels

The immutable `AllowUnpaired` policy is not a live revocation API. Any future
setter must linearize admission/revocation and emit `pairing_required` before
closing relying sessions, not disguise cancellation's `shutdown` as revocation.
Printed product QR labels remain a separate credential-provisioning/packaging
obligation; the standalone audio probe is unrelated.

See [review decisions](rc1-review-decisions.md) for the retained assumptions and
prepared, unsubmitted clarification questions. None is silently resolved by this
extension design or by passing simulated-output/network tests.
