# Official harness, local SDK adapter

Run from a complete checkout with the mise toolchain and a C compiler for Go's
race detector:

```text
mise run test:conformance
go run ./internal/conformancetestcmd --scenarios server-initiated-protocol-baseline-v1,server-initiated-audio-chunk-framing
```

The runner builds a race-enabled executable from `internal/conformanceclient`.
`internal/conformancetest/uv.lock` provisions the official
[Sendspin/conformance](https://github.com/Sendspin/conformance) harness at
`fa6dc45795f0810c5810af9f9b828595b7267ece` and `aiosendspin[server]==10.2.1`.
The local registration overlay uses **sendspin-player-go**, not the unrelated
upstream **sendspin-go** entry. It does not edit an upstream checkout, alter
verdict functions, submit results, or publish a matrix/site.

The unchanged upstream `run_case` orchestration and comparators judge 15 cases:

- PCM16 in both connection directions; server-initiated PCM24, FLAC and Opus.
- The protocol baseline's five assertions and the 13-byte audio-chunk framing.
- Client-initiated preferences from PCM24 to PCM16 and from PCM to FLAC.
- Metadata, controller (`next`) and artwork in both connection directions.

The fixture is generated non-silent, one-second, 48-kHz stereo 16-bit FLAC,
not downloaded music. PCM/FLAC compare decoded source hashes; Opus compares
encoded packet hashes, not lossy decoded samples against the source. The SDK's
real player, decoder and scheduler run against a bounded clock-simulating sink;
there is no real sound device and no measured physical timing.

## Safety and evidence

Provisioning requires network access. After provisioning, these cases use only
loopback sockets and a per-case JSON registry, not mDNS/LAN discovery. The adapter
refuses non-loopback registry endpoints. Ports are selected ephemerally; like the
upstream numeric-port contract, they cannot be reserved across process startup.
The test identities/PSKs use the harness's public deterministic recipe and exist
only in memory. Never use these identities or credentials on real devices.

The Go adapter uses the public SDK API, not a replacement protocol client.
`Config.Observe` supplies projected transport observations; the baseline checks
actual directions, frame types, capabilities, activation, initial state and audio
headers. Missing, truncated or ambiguous fragment evidence cannot pass. Header
records are paired by timestamp with `Config.Encoded`'s actual payload slices;
duplicate timestamps fail rather than inventing a payload boundary. The decoder
hook hashes actual decoded float32 samples before gain/drift correction.
Unit tests deliberately remove or corrupt evidence and check failed verdicts.

Results, summaries and logs stay under `var/conformance/run-*`, excluded
from the distributable SDK module. `sdk-run.json` records the harness/reference
versions, generated-fixture scope and every case result. The assertion contract
still calls its revision `8c9577ea8719ad082d051ec13cc73ef15ed68948`; this is **not**
the SDK's normative RC1 revision `671a34d408581fc25ea56b3528a4a3f13e3be901`.
Both are recorded separately, as required by the pinned harness.

The runner waits for child adapters and kills remaining tracked children on
ordinary failure/cancellation. Forced termination of the whole process tree is
not an orderly shutdown. Linux execution is locally verified; Windows/macOS
runtime and cancellation behavior remain unverified (CI initially runs Linux).

## Limits

Passing this selected local suite is **not full RC1 certification**. It does not
cover every harness scenario, pairing UI/code methods, discovery, multiserver
arbitration, physical synchronization, native backend behavior or sustained load.
Some upstream evidence is supplied by the reference SDK's own hooks/guards.
The independent SDK E2E/fault suite and isolated multicast suite remain separate
and complementary. See [the coverage matrix and remaining audit](conformance.md).
