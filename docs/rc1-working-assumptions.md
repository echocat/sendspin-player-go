# Provisional RC1 implementation policies

We can continue implementation and targeted testing without pretending that every
normative interpretation is resolved. The machine-readable policies are
`working_assumptions` in [the catalog](rc1-requirements.json); the generated
[audit](rc1-audit.md#provisional-working-assumptions-not-normative-evidence) and
`var/audit/report.md` expose their declared and executed evidence separately.

## Comparison baseline

Source-inspected implementation: [Sendspin/sendspin-cpp at
`985fba57afb0462b520ab9ce974d1860858ed685`](https://github.com/Sendspin/sendspin-cpp/tree/985fba57afb0462b520ab9ce974d1860858ed685).
Every catalog reference pins that commit, file and line range. This is **not** the
standalone C++ time-filter oracle already used by our synchronization tests, and
it does not replace specification revision
`671a34d408581fc25ea56b3528a4a3f13e3be901`.

No complete RC1-conformance claim is made for the C++ checkout. Its observations
are source inspection, **not executed C++ tests or new C++ interoperability**.
Our new native policy test still uses the pinned independent `aiosendspin` peer.

## Selected working policies

| ID | Provisional decision | Important difference / boundary |
| --- | --- | --- |
| WA01 | Keep static application-selected timing hints; test actual outgoing values through readiness/availability changes. | C++ also reports configured/derived values, but its 500-ms default is not a measured optimum or a new Go default. |
| WA02 | Keep nonnegative progress/duration/speed, strict complete-object rejection and exact int64 values. | C++ uses uint32 and drops invalid fields. We copy neither its 32-bit limits nor its permissive defaulting. Negative/reverse playback remains an open normative question. |
| WA03 | Retain PSK-only pairing until a host explicitly selects an operator interface. | An audio output is not automatically a usable code-emission channel. C++ also requires configured channels/formats; its implemented code methods are not claimed by our tests. |
| WA04 | Retain immutable, locally owned unpaired-access policy per Client. | C++ has a real live setter with relying-session closure. Our advertisement/remote-mutation tests do not prove live revocation, and shutdown is not that operation. |
| WA05 | Keep token encoding in the SDK and physical QR presentation in the product. | A correct token string is not physical printed-label evidence; dynamic QR callbacks do not settle the separate printed-PSK rule. |
| WA06 | Keep bounded pre-Noise cap closure; do not introduce cleartext application goodbyes. | The inspected C++ nursery-full path can send a text goodbye before Noise. That conflicts with our pinned initial sequence and is deliberately not adopted. |

Each policy has its own reason, exact comparison references, selected tests and
an explicit trigger for reconsideration in the catalog. No runtime behavior was
changed to introduce these policies: this pass freezes the currently selected
profile and makes its assumptions testable and auditable.

## What these policies do not do

- Their test results do not change an atomic obligation's `O/C/P/T`, normative
  level, applicability or verification modality, nor close a partial group review.
  Separate [source-based review decisions](rc1-review-decisions.md) may classify
  an obligation while leaving its missing evidence and strict blocker intact.
- Passing an assumption's tests does not supply missing evidence to its linked
  atom. Conversely, a passing atom does not silently test an assumption.
- They do not establish forward secrecy, physical accuracy, hardware audibility,
  network/load behavior or native platform coverage.
- They do not remove open questions from the backlog or relax the strict
  conformance gate. Three group classifications remain partial (P08, CN15, MD01);
  classified security/withdrawal/printed-QR duties still have missing evidence.

The checker requires `provisional`, existing atomic/test links and full-commit
comparison URLs. Changing a policy changes evidence fingerprints. Ordinary audit
checks are offline: URL shape validation does **not** claim to have re-fetched or
executed upstream sources; updating a comparison pin requires source reinspection.

For now we can work through compatible software-evidence gaps while leaving the
interpretation questions visible. Reopen a policy before implementing a live
setter, estimator, code-pairing UI, different metadata domain or resource model.
