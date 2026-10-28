# RC1 cryptographic implementation review

This is an implementation/source review, not an independent cryptographic audit,
formal security proof, entropy certification or a claim about compromised live
process memory. **CN19.04 remains an open assurance blocker.**

## Frozen primitive sources

Reviewed `github.com/flynn/noise v1.1.0`, commit
`4d9f71cd4ba1fe81415efac312664ccc4bc79b46`, the version in `go.mod`/`go.sum`.
Source bytes were fetched from that commit, not a moving branch:

| Source | Git blob | SHA-256 |
| --- | --- | --- |
| `patterns.go` | `7d29d5ba75347a879a3c3660b7c562fa4c81084a` | `6e5ec0b36e9f3cc171b9580622957d32bbef0ee0b6c2b5fe5126881a5c801001` |
| `state.go` | `00d6b96631fd9cf2e254faae738d553c4c296aa0` | `d0c72081ef758739ffb714b0c0519bb1164bf84a3d945e8c063a54c1e46590bb` |
| `cipher_suite.go` | `753e011f67aacbb5f2480307d4d852cc128d8501` | `3c0098a66edfdafd5f74295e993b4da235aa8b6f9c70f32c37d8dc7acb554bf3` |

## Findings and trace

1. **Pattern/roles.** `patterns.go:29-36` defines KK with known static keys;
   message 1 is `e, es, ss`, message 2 `e, ee, se`. `state.go:336-353` appends
   the PSK token to message 2. SDK `internal/wire/channel.go` creates a new
   handshake state for every exchange, explicitly sets responder/KK/psk2 and
   chooses only the two RC1 suites. Both WebSocket directions use this path.
2. **Randomness/freshness.** `state.go:405-410` generates a fresh ephemeral from
   the configured reader; `cipher_suite.go:107-119` reads 32 bytes and computes
   the X25519 public key. The SDK now explicitly supplies `crypto/rand.Reader`
   rather than relying only on the reviewed library default (`state.go:327-329`).
   No ephemeral keypair is injected or stored in `Channel`/`Credentials`.
   `TestHandshakeRandomnessAndResponderPolicyAreExplicit` guards this policy.
3. **Transport key dependency.** Message 2 mixes ephemeral/ephemeral DH
   (`state.go:424-429`), followed by the other required tokens/PSK, before
   `Split` (`state.go:197-201,478-480`). Thus the reviewed construction retains
   the ephemeral-DH dependency expected from KKpsk2. This reasoning assumes the
   security of X25519, HKDF, AEAD and actual OS randomness; executing a roundtrip
   cannot prove those assumptions.
4. **First-message exception.** Message 1 precedes PSK incorporation and the
   responder ephemeral. The client static secret plus the recorded public
   transcript can recover its payload. Do not extend the transport claim to
   this payload or mistake the public Sentinel PSK for peer authentication.
5. **Counters.** `state.go:47-81` checks the nonce limit and advances the counter
   after successful encrypt/decrypt. SDK transport cipher states are private,
   not exported/resumed/reset with `SetNonce` or `UnsafeNewCipherState`.
   Replay, skipped/out-of-order frames and tampering are already exercised via
   actual public handlers, separately from this source review.
6. **Commit/rekey.** Raw init bytes form the initial prologue; rekey uses the
   previous binding hash. New ciphers/category/hash publish only after message 2
   is sent successfully. The actor's reader acknowledgement prevents an old-key
   read racing a rekey; activation gates new-key application processing.
7. **Persistence.** The Store holds long-lived identity/pairing material and
   product settings, not transport ciphers/nonce counters/ephemerals. Identity
   and device PSK generation use OS CSPRNG; long-term PSKs use `crypto/rand.Read`
   in `session.activate`, independently guarded by
   `TestLongTermPairingSecretGenerationUsesCSPRNG`. FileStore uses private Unix
   files/atomic replacement; Windows directory ACL protection is host-owned.
8. **Logging.** Default SDK logs contain lifecycle categories, counts and booleans,
   not raw errors, endpoints, names, tokens, handshake payloads, media or Store
   objects. Recording-logger tests include real impaired encrypted sessions.
   Explicit `Observe`/`Encoded`/`Decoded` hooks remain separate application-owned
   diagnostics, not automatic logging sinks.

## Limits and residual work

The subsequent [independent Cacophony vectors](noise-vectors.md) now check exact
foreign handshake/transcript/transport bytes for both suites, direction/counters
and fault cases offline, with an explicit pinned-source verifier. They strengthen
primitive compatibility evidence but do not promote CN19.04 to independent
forward-secrecy certification.

- This review and AST regressions do not independently validate the primitive or
  prove forward secrecy. An external/security-assurance review remains distinct.
- Go garbage collection does not guarantee zeroization of old heap/stack copies.
  No promise is made for live-memory compromise, process dumps, swap, debugger or
  OS compromise. Long-lived Store secrets remain sensitive backup material.
- Session confidentiality/replay protection does not make unpaired Sentinel
  sessions resistant to MITM. Static identity binding and operator pairing matter.
- Arbitrarily blocking Store, output, observer or logging implementations cannot
  be forcibly preempted safely. Cooperative deadlines are not hard-real-time I/O.
- No algorithm, suite, wire version, pairing scope or trust policy was replaced.

Reviewed sources: [pattern][pattern], [state machine][state], [DH/AEAD][cipher].

[pattern]: https://github.com/flynn/noise/blob/4d9f71cd4ba1fe81415efac312664ccc4bc79b46/patterns.go
[state]: https://github.com/flynn/noise/blob/4d9f71cd4ba1fe81415efac312664ccc4bc79b46/state.go
[cipher]: https://github.com/flynn/noise/blob/4d9f71cd4ba1fe81415efac312664ccc4bc79b46/cipher_suite.go
