# RC1 review decisions

Baseline: Sendspin/spec **1.0.0-rc1** at
[`671a34d408581fc25ea56b3528a4a3f13e3be901`][spec]. This review changes audit
classifications and evidence reporting, **not SDK runtime behavior**. The 148
historical assessments and all 742 atomic IDs remain intact. A completed
classification is not fulfillment, a security audit or product certification.

The original 27 strict-gate entries combined group-level, inventory-level and
atomic blockers; they were not 27 independent implementation bugs. The separate
backlog had 14 review items. All 14 are dispositioned below, including the ones
that genuinely still need clarification or integration/security evidence.

## Source and interpretation boundary

The [Normative Language section in template.md, lines 22-24][language] says BCP 14
keywords have their special force only in uppercase. This context is now pinned
separately in `interpretation_sources` in the catalog, including Git blob,
SHA-256 and line count. `audit:verify-spec` checks it at the same RC1 revision as
the nine indexed documents. The subsequent `rc1-template-inventory.json` supplement
now separately inventories all 169 source lines and 34 predicates for definitions,
role-versioning and evolution. A fingerprint alone never implied that review.
Overall normative review stays partial for the open primary interpretations and
external referenced definitions/assurance; the supplement does not inherit passes.

That capitalization rule does not make declarative field types, sequences or
security contracts optional. It does distinguish an ordinary lower-case
suggestion from an uppercase BCP-14 SHOULD when that is what a sentence says.

## Dispositions of the 14 review items

| Atomic ID | Classification decision | Work still owed |
| --- | --- | --- |
| `P08.04` | Keep the conditional SHOULD to debounce measurement-driven hint changes. Immutable hints do not generate those updates. | Reviewed current API boundary, not estimator evidence. Reopen when adding measurement-driven updates. |
| `P08.05` | Still unresolved: the derivation sentence precedes explicit estimator SHOULDs. | Clarify whether static, deployment-chosen hints satisfy the declarative derivation contract. No estimator is silently added or declared optional. |
| `P08.07` | Keep the conditional SHOULD to discard preconvergence delay-estimator samples. The SDK collects no such samples. | Reviewed current sampling boundary only. Reopen with any estimator; `P08.05` remains independent. |
| `CN15.02` | Still unresolved: an early cap closure cannot send an encrypted lower-priority refusal. | Clarify the pre-Noise cap/refusal boundary without inventing a cleartext application goodbye. |
| `CN19.04` | The declared forward-secrecy property is a mandatory transport contract, not an unclassified optional feature. | Manual cryptographic assurance review remains open; ordinary software passes do not discharge it. |
| `R01.08` | Preserve the uppercase dynamic-over-static SHOULD for usable out-channels. | The PSK-only profile leaves a documented recommendation gap. Adding a display/speech code method remains a separate scope decision. |
| `R01.09` | The following QR sentence uses lowercase `should`, so it is informative product guidance, not an extra BCP-14 SHOULD. | Dynamic code is unselected. This does not exclude the uppercase printed-PSK QR MUST or selected QR-method wire rules. |
| `R13.05` | Local withdrawal has a conditional mandatory `pairing_required` closure contract; the text does not require a particular setter API. | Review and implement that exact operation if an embedding product offers live withdrawal. Immutable-policy tests and cancellation do not prove it. |
| `R15.04` | A physically printed PSK has a mandatory QR/token packaging contract, not a requirement for an SDK printer API. | Inspect/decode an actual product label or leaflet if one is printed. Token-string tests are not physical packaging evidence. |
| `MD01.06` | Negative `track_progress` remains an unresolved wire-domain question. | Clarify validity/rejection versus preserving the integer and clamping only the displayed result. |
| `MD01.07` | Negative `track_duration` remains independently unresolved. | Clarify its domain and treatment; zero's unknown-duration meaning does not define negative values. |
| `MD01.08` | Negative `playback_speed` remains independently unresolved. | Clarify reverse-speed validity and receiver behavior. No reverse-playback semantics are invented. |
| `M19.02` | Host-owned availability policy: an interruptible activity should remain available. | The SDK contract is reviewed. Each product must classify its actual external activities, not expect automatic detection. |
| `M19.04` | Host-owned availability policy: unavailable only while unwilling to yield, then promptly available again. | The SDK contract is reviewed. Product willingness/yield timing is not certified by a virtual output or API test. |

The resulting **145 complete group classifications** leave **P08, CN15 and
MD01 partial**. CN19, R01, R13, R15 and dependent M26 are now classified, not
declared fulfilled. In particular the mandatory `CN19.04`, `R13.05` and `R15.04`
rows remain strict-gate blockers. The numeric/timing/cap working policies stay
provisional; no existing requirement gains a `T` assessment from this review.

Existing upstream discussions were also read, not submitted or modified:

- [#184's maintainer decision][method-discussion] says the client may choose its
  pairing method and screen/QR advice belongs in an implementation guide.
  [#205][guide-discussion] records that deferred guidance. These explain the
  PSK-only profile, but do not erase the uppercase SHOULD still present in our
  pinned R:19 text or silently update its wording to a later revision.
- [#156][timing-discussion] motivates per-chunk transmit timing with intermittent
  Wi-Fi interference. Its resolved timestamp request is not an answer to the
  separate question whether the player must run an online buffer estimator.

## Integration checklist (not universal product evidence)

### Availability: M19.02 / M19.04

- List local activities competing for output and identify which may yield to
  Sendspin. A device with a physical input selector is not inferred from the SDK.
- Keep `SetAvailable(true)` while willing to yield. Set false only for the period
  in which the product will not yield; restore true when that changes. The SDK's
  own clock/readiness gates still apply to the transmitted state.
- Check the real ownership handoff and the host's return-to-available path. A
  successful state serialization does not establish that a product actually
  yielded, nor impose a universal deadline on arbitrary application callbacks.

### Unpaired access: R13.05

- `AllowUnpaired` is immutable for one Client. Remote fields cannot turn it on
  or off, and constructing another Client does not mutate the old one.
- Do not expose a live-revocation UI by merely cancelling the old Client:
  cancellation means `shutdown`, not `pairing_required`.
- A product implementing live withdrawal must identify every relying unpaired
  session, send the specified encrypted reason and close it without terminating
  unrelated paired sessions. Define admission/withdrawal race ownership and
  assert the actual wire reason and closure before claiming that operation.
- There is no live-withdrawal API or claim in this SDK profile. This review does
  not authorize adding one or treat an absent operation as tested revocation.

### Printed credentials: R15.04

- First determine whether the product actually prints a device/leaflet PSK.
  Copy/paste and a console token are not automatically physical printing.
- If printed, the QR symbol must carry the version-0 pairing token verbatim,
  without a URI wrapper or bare PSK. Decode the actual symbol and compare the
  identity and PSK with the corresponding provisioned device.
- Review the real artwork/label and provisioning/secret-handling process. Do not
  put a live token, private key, PSK or label image in public test artifacts.
- No printer, QR-rendering feature, physical label or capture is introduced here.

### Cryptographic assurance: CN19.04

- Review the pinned `github.com/flynn/noise` implementation and the SDK's
  `internal/wire/channel.go` configuration: KK responder, `psk2`, selected
  X25519/AEAD/SHA-256 suite, PSK category/binding and fresh handshakes.
- Trace transport key derivation through ephemeral DH and cryptographic
  randomness, including rekeys and secret retention/persistence. Inspect the
  stated message-1 static-key exception separately from transport protection.
- Define the compromise model. A static-key/PSK disclosure is not the same as
  obtaining live ephemeral keys or process memory; Sentinel confidentiality is
  not authenticated peer identity or MITM resistance.
- Record who reviewed what and any limits. Interop, tamper/replay tests, distinct
  ciphertexts or shared-primitive roundtrips are not an independent security
  proof. None is promoted here. The mandatory manual review remains visible even
  after the group's classification is complete.

## Prepared clarification questions (not submitted upstream)

These questions cite the **pinned** text. A response or a later spec change must
be reviewed explicitly; it does not silently replace the SDK's RC1 baseline.

### 1. Capacity before Noise — CN15.02

Sources: [connection.md, lines 34-42][capacity] and the [initial message sequence
and silent-failure rules][messaging]. The selected SDK cap can refuse a new
socket before Noise; no encrypted application channel then exists.

Does the permission to cap provisional connections cover pre-Noise connections,
and if so may these close silently (or refuse the HTTP upgrade)? Or does
"as if they were lower priority" require completing Noise before sending the
encrypted `concurrent_attempt` refusal? How should that interact with the
record-capacity bound and already unfinished handshakes? We deliberately do not
send a cleartext `client/goodbye` to work around this conflict.

### 2. Static versus measured buffering — P08.05

Source: [player.md, lines 63-65][timing]. Timing updates are MAY, measurement
debouncing/tail sizing/preconvergence rejection are SHOULD, but the preceding
sentence says a player derives `min_buffer_ms` from chunk-arrival distribution.

Is an online arrival-delay estimator required for every player, or are static
application/deployment-selected values supported? If required, is derivation from
offline deployment measurements sufficient, and which requirements apply before
enough converged samples exist? The SDK presently keeps immutable hints; this
review neither installs an estimator nor certifies a measured optimum.

### 3. Metadata integer domains — MD01.06 / .07 / .08

Source: [metadata.md, lines 17-20 and the position formula][metadata]. These
fields say `integer`; unlike player timing hints they do not say non-negative.
The supplied position formula clamps the displayed result at zero.

Please specify each field independently:

1. Can `track_progress` be negative, and should a receiver preserve it and clamp
   only the calculated position or reject the entire state?
2. Is a negative `track_duration` invalid, or does it have a defined result distinct
   from zero/unknown? For a literal reading of the formula its final clamp yields
   zero, but that does not itself define the intended wire domain.
3. Can negative scaled `playback_speed` represent reverse playback, and what is
   the required receiver behavior if it does not?

The current complete-object rejection and nonnegative int64 policy remain
unchanged under WA02. Tests of that policy are not its normative justification.

[spec]: https://github.com/Sendspin/spec/tree/671a34d408581fc25ea56b3528a4a3f13e3be901
[language]: https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/template.md#L22-L24
[capacity]: https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/connection.md#L34-L42
[messaging]: https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/messaging.md#L1-L17
[timing]: https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/roles/player/v1.md#L63-L65
[metadata]: https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/roles/metadata/v1.md#L17-L46
[method-discussion]: https://github.com/Sendspin/spec/issues/184#issuecomment-5454033013
[guide-discussion]: https://github.com/Sendspin/spec/issues/205
[timing-discussion]: https://github.com/Sendspin/spec/issues/156
