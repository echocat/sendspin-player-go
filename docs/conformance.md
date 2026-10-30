# RC1 implementation and test coverage

Normative baseline: Sendspin/spec tag `1.0.0-rc1`, commit
`671a34d408581fc25ea56b3528a4a3f13e3be901`. SDK releases, core wire version `1`,
and versioned roles such as `player@v1` are separate concepts. Wire constants live
under `internal/protocol`, codecs and device output are independently replaceable.
Future revisions must be added explicitly, without changing RC1 behavior or
inventing version fallback that RC1 does not negotiate.

This document records the present scope, **not full certification**. The reference
server can have defects; successful interoperability is not normative proof.

**Version boundary:** `v0.1.1` remains the recommended stable release. The opt-in
Source, PIN and host-managed player-control APIs and the purego native-backend
migration described below are **unreleased**; installing `@v0.1.1` does not provide
these new APIs. Native input/output implementations are still work in progress.
The newly configured Linux/macOS/Windows amd64/arm64 backend CI matrix has **not
run** for this unpushed work. Local software tests and cross-builds are not hosted
CI results, native execution on another OS, or physical audio measurements.

The [traceability system](traceability.md) now validates the machine-readable
catalog, generates the clause tables, and correlates actual test/harness results
with their source/toolchain fingerprints. See `var/audit/report.md` / `report.json`
locally or the **rc1-traceability-report-attempt-N** CI artifact. Unrun, skipped, stale and
failed evidence is not silently treated as a pass; manual partial assessments
and the incomplete normative inventory remain separate from execution results.

The atomic normative pass now covers all **148 historical groups** without
deleting their IDs or promoting their historical assessments. **145 classifications
are complete; three remain partial** (P08, CN15 and MD01). The
[remaining-review decisions](rc1-review-decisions.md) separate resolved
classification from still-unfulfilled security, withdrawal and printed-QR evidence. Individual
obligations distinguish actor, MUST/SHOULD/MAY/prohibition, conditional scope and
software versus hardware verification. The generated report separately prioritizes
implementation opportunities, missing targeted assertions, measurements and open
normative decisions, and lists groups still lacking completed atomic review.
Classification complete is **not** fulfillment complete; server obligations and
unselected optional branches remain visible without becoming client SDK bugs.

Controller command/state constraints are now separated from the server's media,
group-volume/mute and switch algorithms. Metadata and color share canonical
state/scheduling/removal obligations; contrast generation remains server-owned.
The existing negative metadata numeric-field rejection is an explicit open
normative-domain question, not certified by the test cases that currently expect
it. No runtime domain/renderer behavior was changed by this review.

We can continue under [explicit working assumptions](rc1-working-assumptions.md),
using pinned `sendspin-cpp` source observations without treating that implementation
as the specification. The policy ledger and its tests are separate from atomic
conformance evidence; in particular its uint32 metadata limits and pre-Noise
cleartext-goodbye path are not adopted.

| Area | Implementation | Evidence / remaining work |
| --- | --- | --- |
| JSON envelope / unknown informational fields | Implemented | Typed-model fuzzing, exact-case field matching, nested null rejection, independent extension/failure injection; empty/unnamespaced unknown types ignored only after activation |
| Core time/leave/group wrappers | Implemented | Native no-role/controller-only hello/time/empty-leave observations; full group/time field matrix while available/unavailable; silent malformed-peer closure without Store writes; unrelated server time echoes do not consume clock requests |
| Server hello / language hints | Implemented | Independent malformed-hello silent-close/no-reply tests; RFC 5646 structure checked against examples and an ABNF fuzz oracle (not registry/extension-semantic validation); verbatim defensive Snapshot.ServerLanguages across re-handshake. Non-source clients validate the source-support outer object only; opt-in Source additionally validates the offered codec list |
| Noise KKpsk2, raw init prologue | Implemented | Independent E2E for both suites; noncanonical JSON-prologue test; canonical wire Base64URL checks reject padding/CR/LF/unused bits before identity selection |
| Handshake faults / activation gates / limits | Targeted evidence | Scripted Go peer in both suites/directions: malformed initial/rekey fields, tamper, prologue/suite mismatch, early applications, missing-message timeouts, unchanged trust/session cleanup; positive full-size 65535-byte Noise handshake and fragmented rekey, then oversized transport rejection. Shares the Noise primitive; not independent crypto evidence |
| Handshake write faults / cancellation | Targeted evidence | 152 I/O cases (76 per direction) across both suites: zero/prefix/full-carrier errors, cancellation/deadline, key/binding commit boundary and failed fragmented hello. Inbound uses actual ServeHTTP, unchanged HTTP upgrade and verified mode reservation/release; real masked/unmasked carriers are checked. Injected net.Conn, not kernel backpressure/native socket coverage; idle fixtures do not roll back legitimate prior saves |
| Replay, tampering, cleartext after encryption | Rejected | Both suites/directions include actual public-handler replay/out-of-order closure and released trust/session/mode state; unknown and unavailable-role fragments still authenticate and validate sequences. These checks do not establish independent cryptographic forward-secrecy assurance |
| Noise fragmentation | Implemented with memory bound | Independent fixed first/continuation/last header vectors and exact carrier boundaries; fitting messages use exactly one unchanged carrier; recursive original ID is rejected before emission; callback errors stop immediately; roundtrip, malformed sequences, unknown IDs and fuzz remain separate evidence |
| PSK pairing, promotion, unpairing | Implemented | Native flow/rekey/unpair, refusal/index/discard/sequence E2E; delayed-first-write timeout and real late-finalize without an actor ticker reject post-expiry disclosure/local persistence; lenient token-padding/extension/ASCII matrix plus fuzzing; blocking-I/O/load timing remains unverified |
| Pairing persistence deadline | Targeted evidence | Attempt deadline is propagated to cooperative local Store.Save; native post-peer-commit/no-actor-ticker expiry clears local pending state and emits attempt_timeout. Earlier parent/store errors are preserved. No remote rollback or arbitrary native/custom-store preemption claim |
| Initial Sentinel fallback / stored identity binding | Implemented | Independent client/server credential-loss and repair E2E; mismatch cannot bypass an existing record, even with unpaired approval; misbinding cannot displace another server |
| Unpaired policy / token boundary | Targeted evidence | Actual enabled/disabled hello bool and inverse remote-policy fields are tested through the native encrypted peer; no runtime toggle is added. A fixed RFC-key/PSK emitter vector is independent of the lenient SDK decoder. Conditional printed-QR and live-withdrawal duties are classified; product evidence remains open. Code methods remain unoffered by default; the explicit PIN extension is separate |
| Static/dynamic PIN pairing | Implemented, opt-in and unreleased | Independent pinned-server tests cover the selected code method, retry/cancellation and counter limits. PSK-only remains the default. Physical operator UI, product acceptance and independent cryptographic assurance remain separate; see [PIN extension](pin-pairing.md) |
| Persistent identity, pairing records, output delay | Implemented | Private file roundtrip plus actual FileStore/New-client restart with nondefault volume, mute and delay observed again by the independent peer; CSPRNG generation-source and distinct-device regressions, not statistical entropy certification |
| Multi-server arbitration / capacity | Implemented | Independent idle/playback/equal-priority/last-server arbitration, active-pairing protection, refused concurrent pairing, buffered-audio handoff, and stalled-peer capacity exhaustion/recovery; deferred displacement through a rejected re-handshake activation; provisional-handshake record eviction and activation-budget tests; more concurrency stress outstanding |
| mDNS advertise / discover | Implemented | Independent IPv4 and scoped link-local IPv6 multicast/encrypted sessions in both directions, unique instances, metadata/withdrawal, actual isolated interface attach/removal and socket cleanup; native Windows/macOS and broader deployment behavior remain separate |
| Clock offset/drift | Reference algorithm port | 22,789 pinned C++ queries across 16 deterministic scenarios, live native comparisons and source provenance; exact-integer NTP boundary/fuzz tests; receive-boundary timing and re-handshake request handling; hardware sync still outstanding |
| PCM / FLAC | Implemented | Independent source hashes and harness PCM16/24/FLAC checks; fixed distinct-channel PCM vectors and buffered PCM→FLAC→PCM receive/decode/render traces; codec_header presence and canonical standard Base64 are checked before custom factories. Built-in FLAC independently rejects ignored CR/LF. Codec contents remain a custom factory's responsibility |
| Opus | gopus v0.2.2 | Bare single/multiframe packet, structural error/container and duration vectors; bit-depth equivalence and independent server negotiation/sample counts. Arbitrary byte concatenations can themselves be one valid packet; exhaustive codec parity belongs upstream |
| Playback queue / clear / correction | Implemented | Bounded recovery/cancellation; exact active/target subset and target-list grammar tests; clear/end preserve untargeted state and validate uncancelled artwork before ending other roles; unknown end fields do not acquire clear-only typing. Overflow-safe scheduling, bounded predicted-output correction and old-format retention are separately tested. Physical speed/accuracy, audibility and rare-resync-under-load remain outstanding |
| Buffered playback lifecycle / rekey | Targeted evidence | Independent encrypted PCM injection with a gated virtual output: clear/end/removal cancel blocked and queued old audio; two native pairing rekeys preserve it without flush/reopen. Worker drain/Open/reset/error paths have deterministic barriers; no physical stop/gaplessness claim |
| Concurrent admission / full record store | Targeted evidence | Atomic holder/rank publication; canceled contenders cannot retire the holder; three native inadmissible peers bypass a deliberately locked admission path. Full five-record/four-session pairing protects three unfinished handshakes; old-record protection survives pending rekey selection until key commit. Not exhaustive resource/load refusal evidence |
| Format preference / in-place stream changes | Implemented | Independent PCM16-to-PCM24 E2E; official harness PCM24-to-PCM16 and PCM-to-FLAC changes; broader transitions outstanding |
| Controller | Implemented | Full state/replacement/seek-bound validation; all 15 command kinds independently observed with exact zero/false/signed-64-bit arguments; native capability changes and role removal/reactivation; known command outer-object checks before player mutation, unsupported source internals opaque; defensive read model; server grouping/backend effects not certified |
| Host-managed player volume/mute | Implemented, opt-in and unreleased | Fake-target/queue/generation and unity-gain tests distinguish requests from actual readback. Non-nil PlayerControl, including fixed/no-controls, disables software gain without automatic hardware writes. No CEC, ALSA mixer, HDMI policy or physical loudness evidence; see [external player control](external-player-control.md) |
| Source PCM16 upstream | Implemented, opt-in and unreleased | Deterministic fake Inputs and the unchanged independent server verify explicit start authorization, exact interleaved PCM16, drift-inclusive timestamps, lifecycle/order, bounded backlog, rekey and reconnect. Explicit unpaired source approval is distinct from playback approval. No local monitoring or implicit capture; native ADC accuracy and a full Source clause audit remain open; see [Source extension](source-input.md) |
| Metadata / color | Implemented | Independent current/scheduled/replaced/cleared states, malformed-field rejection and exact large paused progress; one-indexed track validation; exact integer position formula checked against math/big and fuzzed; RGB domains, defensive snapshots, role removal/reactivation and first-state clock-estimate recovery tests; clock/display timing limitations remain |
| Artwork | Implemented | Independent multipart PNG/JPEG hashes, dimensions/padding, multiple/all-disabled channels, native mid-transfer cancel/replacement/role removal with real transmit barrier, scheduled replacement/cancel/clear, end/resume and malformed wire/config faults; defensive reads/discard accounting; explicit local size limit and broader load/clock/rendering limitations remain |
| Visualizer | Implemented | All five reference-packed vectors, native beat schedule, strict conditional/full spectrum schema, non-decreasing cross-type timestamps through updates, reserved-bit/ID handling, stale reception/event-barrier checks and clear/end/removal; queue ownership/byte invariants; analysis/rate/physical rendering quality not certified |
| Exact hello/state output | Targeted software evidence | Both cipher suites and all 64 subsets of the initial six-role profile have independently expected hello fields/support objects; all 64 active subsets have exact state output before/after readiness and while unavailable. DeviceInfo omission combinations, positional artwork arrays, reactivation and preferred-format retention are checked through encrypted scripted-peer messages, not inferred from peer acceptance. Opt-in Source has separate tests, not an inferred exhaustive seven-role subset matrix |
| Presentation lifecycle across rekey | Targeted software evidence | Same-version explicit/sticky role activations and two actual rekeys preserve metadata/color/controller, complete pending artwork, another in-flight image and queued decoded visualization. Old transfer completion uses new keys and due updates emit once in order. Directly driven actor and controlled clock; no physical display/timing/native-stress claim |
| Native output/input | Unreleased purego migration, WIP | Root packages audio/alsa, audio/pipewire, audio/wasapi and audio/coreaudio target CGO-free system APIs; audio/native is an optional selector module. ABI/fake/null-PCM/private-software-graph checks and cross-builds must be attributed to the tested revision. The configured new matrix is unrun; no hardware tests were performed for this migration. See [native test contract](native-audio-tests.md) |
| Explicit no-device output | Unreleased audio/noop | Paced discard output for software use, with no capture implementation and no physical clock evidence. Never an automatic fallback for a failed native backend |

Historical Oto/PortAudio results and the [Oto feasibility experiment](oto-feasibility.md)
describe earlier implementations, not the current backend migration. Those
backends are being removed from the development tree; their past results and
release tags are not rewritten and cannot establish evidence for replacement
purego backends. Software timing/flush tests never substitute for physical
device-clock, downstream-stop or audible-quality measurements.
Backend-path migration does not promote the frozen P13.04 assessment: it remains
partial (`P`), with physical evidence still missing.

### Valid sessions versus fault injection

The normal E2E scenarios use the reference server's real handshake, pairing and
role APIs. Separate fault tests deliberately enqueue malformed application
messages through its encrypted writer. They verify rejection and the distinction
between required `client/goodbye` reasons and silent connection closure. These
faults are not evidence that the reference server normally emits invalid messages.
Invalid Noise fragment sequences are tested at the encrypted Go channel boundary:
the reference server correctly refuses to send transport-reserved fragment IDs
through its application API, so that guard is not bypassed or patched for E2E.

Media scenarios use the reference role APIs and encoded-byte observations at the
reference sender. Artwork tests compare the complete encoded image and decode it
with Go's image packages; large PNGs force multiple parts below the Noise size cap.
Visualizer vectors use the reference packer and normal role queue; beats use its
native schedule/drain path. Queue-drain barriers followed by unique group updates
prove earlier messages reached the Go actor before testing pending-state behavior.
These checks validate protocol handling, not display latency, FFT accuracy or
physical audiovisual synchronization.

### Time-filter parity

The Go port is checked against unmodified upstream C++ at revision
`531e2707a6e82f30efe335b0f87eaa4b29390021`. Frozen C++ results run in normal Go tests;
the live test recompiles the source with native GCC/Clang-compatible C++17 and
compares the complete traces. Both source Git blob IDs, SHA-256 content hashes,
input digests, compiler flags and generator fingerprint are recorded/validated.
The 16 scenarios include drift, jitter, outliers, clock steps, gaps, rejected
samples, reset and large clocks. See the [oracle contract](../internal/timesync/testdata/reference/README.md)
for column definitions, well-defined-input limits and rounding tolerances.

NTP four-timestamp arithmetic is a separate Go helper, not part of that C++ API.
It is compared to arbitrary-precision integer calculations, including mixed-sign
rounding and int64 extremes; unrepresentable offsets are rejected. Neither oracle
parity nor those arithmetic properties prove physical output synchronization or
eliminate the time-filter's network-asymmetry/model limitations.

### Discovery

`mise run test:mdns` compiles a race-enabled Linux fixture and runs real Go/Python
multicast on a private internal Podman/Docker network. It does not announce test
players on the host LAN. Both DNS-SD directions are followed by an actual encrypted
Sendspin session; peer-observed TXT/SRV/address data and goodbye removal are checked.
Tests verify cancellation/socket cleanup and enforce inbound/outbound mode separation.
See [discovery behavior and test boundaries](discovery-testing.md) for safety and
the remaining IPv6/native-platform limitations.

## Scope changes and remaining exclusions

Source capture and static/dynamic PIN pairing were outside the initial released
profile but are now implemented as explicit **unreleased opt-ins**. They remain
disabled by default. Their supplemental tests do not promote the frozen primary
inventory's historical assessments; remaining source/security/product/hardware
review is still required. The following remain outside the current implementation:

- Legacy unencrypted draft protocols.
- Automatic network-timing calibration and automatic server-selection UI.
- A packaged production player application.
- Built-in CEC, ALSA mixer, HDMI policy or automatic host-volume synchronization.

## Local official-harness integration

`mise run test:conformance` runs 15 selected cases against a pinned, unmodified
Sendspin/conformance runner/verdict implementation. All 15 passed locally on Linux,
including five protocol-baseline assertions and actual audio-header/payload-size
evidence. The local adapter registers as **sendspin-player-go**, separate from
upstream's unrelated **sendspin-go**. No upstream submission has been made.
These are existing local harness results, not a rerun or hardware validation of
the WIP backend migration; a changed source fingerprint requires fresh evidence.
See [pins, commands, evidence boundaries and exclusions](conformance-testing.md).

## Normative audit progress

The [first clause audit](rc1-audit.md) maps core messaging/lifecycle and player
requirements to implementation and named tests, separately identifying server-only
obligations, recommendations, incomplete evidence and still-open field validation.
The initial 75 paragraph gaps in the nine indexed files now have explicit
requirement references or individually justified context/example dispositions.
The report retains that distinction; this is not an automatic implementation pass.
It also covers common pairing/PSK clauses, conditional activation fields and
the controller chapter, with connection/discovery/admission/encryption classified
separately. The completed software-evidence pass includes targeted malformed
admission, handshake/error, PSK/resource and scheduling assertions. Remaining
classification questions, independent security/integration assurance, broader
network/load validation and physical evidence stay open. The role mappings are
not exhaustive certification.
Test success must not close an open audit
item without reviewing the requirement it actually exercises.

## Before a conformance claim

1. Complete the remaining independent error, role and multi-server scenarios.
2. Expand the local official-harness adapter's selected scenarios where applicable;
   any upstream registration/submission is a separate, explicitly authorized step.
3. Finish the [clause audit](rc1-audit.md) for every applicable normative requirement,
   including direction/sequencing, conditional fields and resource-pressure behavior.
   The coverage table alone is not a clause-by-clause MUST/SHOULD audit.
4. Run hardware/loopback tests on each supported backend/OS. Measure output error
   against time-filter predictions, the +/-1ms floor, correction audibility,
   startup behavior, jitter, underruns, and sustained sample-clock drift.
5. Run and inspect the configured amd64/arm64 Linux/macOS/Windows jobs for the
   exact source revision being released. This new matrix has not yet run for the
   unpushed migration. Neither old CI results, local cross-builds nor subsequent
   hosted ABI tests establish physical audio behavior.
