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
