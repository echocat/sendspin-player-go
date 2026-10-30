# CPace draft-21 independent vector

`draft21-x25519.json` is decoded verbatim from appendix B.1.9 of
https://www.ietf.org/archive/id/draft-irtf-cfrg-cpace-21.txt
(SHA-256 `ed2772c26c21d43a199d490c1ebe5c5d2431a7dbce50d2124d4fa40957fbf58f`).
The source is copyright 2026 IETF Trust and the document authors, subject to
BCP 78 and https://trustee.ietf.org/license-info. This fixture contains data,
not copied implementation code. Runtime field operations come from
filippo.io/edwards25519 v1.2.0 and X25519/SHA/HMAC from the Go standard library.
Vector agreement and independent Python interoperability are not an independent
cryptographic review or a production-security certificate.

`draft21-x25519-coordinates.json` is appendix B.1.10's complete 12-coordinate
set from the same source. Despite its JSON labels, only 0..5 and 7 require
abort; 6 and 8..11 have specified nonzero high-bit-set RFC 7748 results.
Tests preserve both the original labels and the normative expected results.
`python-mcf.json` is the explicit MCF output computed
from the fixed B.1.9 inputs by cpace 0.1.0, already pinned in
`internal/e2e/uv.lock` (wheel SHA-256
`9fabb60a711a85934225be4081b3f5994e1ca4cd1335c5b9171770c8f1a2fda3`).

## Larger independent differential corpus

`python-differential.json` is generated solely by the pinned, unmodified Python
CPace implementation, using public deterministic TEST inputs/scalars, never the
Go implementation. Corpus SHA-256:
`3e8f17dbfc8e363d8eb60d0f655ff512f236e94ed9306006fa688670e162836d`.
The Python module source SHA-256 is
`ee207059c29923d1b0aad50ab3b252a475e44424e130849cef85b86f7755bbd9`.

- 64 exact Sendspin-profile cases: six/eight digits including leading zeros,
  fixed server/client labels, Noise-bound session IDs, activation-index and
  round boundaries.
  Equal-share cases with distinct role labels remain valid; they are not
  incorrectly conflated with reflected equal-AD tuples.
- 64 draft-boundary cases: binary inputs, empty and multi-byte-length fields,
  SHA-512 password-padding transitions and scalar-clamping edges.
- 42 mismatched password/context/SID/AD/share cases and two reflected tuples.
- Expected generators, public/shared points, both ISKs/MCF tags and both peer
  acceptance decisions come from the independent oracle. Go tests also enforce
  no export before confirmation, single-use state and cleanup after rejection.

Verify with `mise run audit:verify-cpace`. The default is read-only/fail-closed:
source/version or frozen-output mismatch fails without rewriting anything.
Only an intentional `uv run --locked --project internal/e2e python
internal/e2e/cpace_oracle.py --write` regenerates fixture bytes. Any such change
requires review and an explicit update of the Go test's frozen hash/inventory.
Dependency provisioning may need registry access; the oracle itself performs
no network protocol/device activity. Ordinary Go tests need no Python/network.

Do not use these public test scalars/PINs in production. This campaign is finite
functional differential evidence, not constant-time/side-channel proof, an
independent security audit, or a replacement of the original IETF vectors.
