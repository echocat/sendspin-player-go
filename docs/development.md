# Development

The repository follows cli4g's library-first layout: public API at the root,
focused public subpackages, private `internal` implementations, independent
`_examples` modules, docs, mise tasks/locks, and GitHub Actions.

Tool requests are in `mise.toml`; exact tool artifacts are in `mise.lock`.
The Python reference environment has its own `uv.lock`. There are no floating
reference-server dependencies in the E2E suite. Do not update its pin without
re-running the tests and checking against the pinned spec, not just server behavior.
The full contributor gate additionally needs a native GCC/Clang-compatible C++17
toolchain for live time-filter comparison. It is not an SDK build/runtime dependency:
ordinary Go tests use the frozen C++ vectors and remain compiler-independent
(apart from the C compiler already required when enabling Go's race detector).
It also needs a running Linux Podman/Docker engine for the isolated multicast
suite. Ordinary Go tests and the loopback reference-server E2E suite do not need
a container runtime or access to the developer's LAN.

| Command | Purpose |
| --- | --- |
| `mise run format` | Format code and tidy dependencies in the SDK, PortAudio module and example |
| `mise run format:check` | Show formatting differences without rewriting files |
| `mise run lint` | Aggregate of lint:go, lint:actions, lint:backends and lint:examples |
| `mise run lint:go` | SDK golangci-lint (including govet), formatting, module files and tagged sources |
| `mise run lint:actions` | GitHub Actions workflow checks |
| `mise run lint:examples` | Independent examples' formatting and module files |
| `mise run lint:backends` | Independent backend modules' formatting and module files |
| `mise run lint:audit` | Validate machine-readable requirements, actual test links and generated audit |
| `mise run format:audit` | Regenerate the human clause tables from their source catalog |
| `mise run audit:report` | Generate var/audit/report.md and report.json from available evidence |
| `mise run audit:verify-spec` | Explicit network verification of pinned source/paragraph fingerprints |
| `mise run audit:check-conformance` | Strict, currently blocked normative/hardware conformance gate |
| `mise run lint:native` | Lint PortAudio plus its example; native development headers required |
| `mise run test` | Unit/integration tests with race detection |
| `mise run test:examples` | Test example selection without CGO or audio hardware |
| `mise run test:native` | Separate PortAudio module and CGO example tests; native headers required |
| `mise run build:example:oto` | Build var/bin/oto/player without CGO |
| `mise run build:example:portaudio` | Build var/bin/portaudio/player with CGO and native PortAudio |
| `mise run test:coverage` | Atomic Go coverage in var/coverage/unit.cov |
| `mise run test:e2e` | Independent reference-server tests |
| `mise run test:conformance` | 15 selected official-harness cases with a local SDK adapter |
| `mise run test:mdns` | Real multicast/Noise E2E in a private internal Linux container network |
| `mise run test:timefilter` | Frozen C++ vectors and NTP arithmetic; no C++ compiler |
| `mise run test:timefilter:reference` | Compile and compare the pinned C++ reference live |
| `mise run test:timefilter:update` | Intentionally regenerate expected values from C++ |
| `mise run test:fuzz` | Bounded envelope/model, RFC language tags, fragments, PCM, media, activation/control/visualizer models, exact metadata positions and NTP arithmetic |
| `mise run test:module` | Extract and test a distributable module zip |
| `mise run vuln` | Reachable Go vulnerability scan |
| `mise run check:full` | Core, CGO-free example and independent-reference gates; native checks remain separate |

`check:full` includes the live reference test but never the fixture-update task.
It checks formatting but does not run the mutating `format` task.
Choose a native compiler with `CXX` (an executable path, not a shell command), or
provide `c++`, `g++` or `clang++` on PATH. Fedora's `gcc-c++`, macOS command-line
tools, and native Windows MinGW-w64/Clang are suitable starting points; Windows
also needs the matching C++ runtime available when running the oracle. In a
Nix-equipped Linux environment the live/full gate can run inside
`nix-shell -p gcc --run 'mise run check:full'` without changing the system compiler.
`SENDSPIN_REFERENCE_UBSAN=1` enables additional C++ undefined-behavior checks.
On Windows/macOS the container runtime must have its Linux VM running. The mDNS
runner prefers Podman and accepts an explicit engine through
`go run ./internal/mdnstestcmd --engine docker`. See
[multicast setup, evidence and safety](discovery-testing.md).
The full gate also runs the [local official-harness adapter](conformance-testing.md),
with its own pinned Python lockfile, generated audio fixture and artifact directory.

The module archive excludes the Python test environment and nested example
modules, as well as the generated-log artifact module. E2E execution needs a complete checkout. Unit tests do not require Python,
a container, multicast or an audio device. Race detection requires a C compiler.

For the optional native backend, install PortAudio development files, then run:

```text
mise run test:native
```

Callback ring tests exercise wraparound, silence on underflow and cancellation
without opening hardware. PortAudio sources/tests use the automatic `cgo` tag,
not a backend-specific tag. Its independent module is not traversed by root tests.
The Oto backend is part of ordinary root tests; they use fakes and require no audio
server. `build:example:oto` explicitly disables CGO only for that build, while the
native tasks explicitly enable it. No global environment or cache setting changes.
Neither this nor a successful build proves physical output synchronization.

Explicit opt-in native output smoke commands are `validate:audio:oto` and
`validate:audio:portaudio`. They write silence only and report actual lifecycle
results separately from device absence, physical measurements and ordinary
callback tests. See [native/physical validation boundaries](platform-validation.md).

## Lint and formatting

`golangci-lint` **2.15.0** is pinned in `mise.toml` with verified artifacts for
Linux amd64/arm64, macOS amd64/arm64 and Windows amd64 in `mise.lock`.
`.golangci.yaml` follows cli4g's v2 configuration: gofmt plus default correctness
linters, asasalint, asciicheck, bidichk, misspell, copyloopvar, exptostd and importas.
Formatting invokes `golangci-lint fmt` directly, including build-tagged sources.
Go does not recursively discover underscore-prefixed directories such as
`_examples`; the existing `go.mod` ignore directives also exclude `_examples`
and `var`. The independent player module therefore has explicit formatting and
lint tasks that run in its own directory. There is no custom formatting wrapper.

`lint` only aggregates `lint:go`, `lint:actions`, `lint:backends` and `lint:examples`.
`lint:go` has separate formatting, module and tagged-source checks; govet runs
inside golangci-lint, not through a redundant `go vet` command.
`lint:examples` aggregates `lint:examples:player`, whose formatting and module
checks are individually runnable. `format:check` reuses those formatting tasks.
`format:go`, `format:backends:portaudio` and `format:examples:player` aggregate separate `:code` and
`:deps` tasks: golangci-lint formats source code, and `go mod tidy` tidies dependencies.

`lint` checks ordinary and e2e/mdns_e2e/timefilter_reference tagged packages without
executing them or requiring a LAN/container/audio device. The native example is
type-checked with PortAudio by `lint:native`; its Oto-only variant is built/tested
without CGO in the OS CI matrix. PortAudio module files and formatting are checked
in ordinary `lint` without compiling native code, and tidied by `format`.
Dependency resolution during lint is readonly. `.idea`, var and Python
environments are excluded; user IDE files are not rewritten.

The narrow exceptions document actual contracts: RC1's `user_cancelled` spelling
cannot be Americanized, and explicitly annotated cleanup error-channel joins must
not replace the primary cancellation/failure. Other cleanup return values are
handled or explicitly discarded; correctness linters are not globally disabled.
CI runs ordinary lint on Linux/Windows/macOS and native lint in its Linux PortAudio job.

## E2E boundaries

The [partial RC1 clause audit](rc1-audit.md) records which requirements each test
actually supports. Keep normative review, independent interoperability, deliberately
injected peer faults, and physical synchronization measurements distinct.

`internal/e2e/server.py` is a thin scenario driver around the **unmodified** Python
server. Its strict mode rejects legacy/nonconforming client messages. It uses
real loopback WebSockets, real Noise, real codec encoders, automatic PSK pairing,
and temporary credentials. No test enables unpaired access to bypass pairing.

Tests check PCM/FLAC source hashes, the reference encoder's final silence padding,
Opus negotiation and sample counts (Opus is lossy, not compared to a PCM source
hash), remote controls, seek/stop lifecycle, persisted output delay, device
identity, paired reconnect, explicit unpairing, in-place PCM bit-depth
renegotiation, competing servers and output ownership during a buffered-audio
handoff. Credential-loss scenarios cover both sides, record preservation, denied
unpaired bypass, and explicit repair. Fault injection covers malformed envelopes,
known null fields, direction errors, invalid activation and audio without a stream;
valid unknown extensions are accepted. The renderer uses an
instrumented fake device. This is not a physical audio-output measurement.

The fixture's control HTTP endpoint exists only on loopback in a short-lived test
process. It is not part of the SDK or an API intended for production deployment.
Source secrets are passed in a private temporary file, not process arguments.

An E2E-controlled Store can pause durable pairing to test a real in-progress
attempt without replacing the reference pairing algorithm. Test-only raw-message
injection uses the real encrypted writer and is kept distinct from normal-server
interoperability. A remote fault must not be inferred from a fixture failure: for
example, the reference writer itself rejects application-supplied fragment IDs.

Pairing activation field tests additionally replace native activation bytes on the
real encrypted writer. The server's native PSK attempt and strict sequence/index
validation stay enabled, allowing refusals and subsequent recovery to be judged
without inventing peer-side attempt state. Optional observation records only
message names, bounded indices and defined abort reasons, never PSKs or Noise data.
Controlled stale messages immediately precede the native cancelling activation;
a same-direction marker proves the actor crossed that discard boundary.

The same opt-in encrypted-writer seam injects malformed `server/hello` fields.
Those cases assert no application reply was sent, keep existing credentials intact,
and remain distinct from normal reference behavior. Positive cases verify exact-case
extension ignoring, defensive language hints, one-time hello sequencing and real
re-handshake preservation. Language-tag fuzzing compares a streaming scanner with
an independent test-only RFC ABNF expression, not a mutable registry service.

Control scenarios use the native reference controller capability setters and
player-state getters. All controller command kinds and exact arguments are
observed at its decrypted receive boundary; numeric command-argument metadata is
returned as decimal strings to avoid losing 64-bit precision in a JSON test client.
The wire still contains integers. Malformed state/command cases separately check
silent closure, unchanged local persistence and no player-state echo, including
while unavailable. Actual server grouping algorithms/backend effects are not SDK
conformance evidence. `FuzzControlModels` exercises the same production parsers.

Media coverage additionally exercises scheduled metadata/palette replacement,
clears and role reactivation, large multipart PNGs, JPEG/disabled channels,
scheduled-image replacement/cancellation, and artwork stream end/resume. Encoded
image hashes come from the independent sender before framing; Go decodes them to
check dimensions and letterboxing. Visualizer tests cover all five wire types,
including native downbeat schedules, and verify that clear/end/role removal discard
future frames. They do not assess the reference server's FFT quality.

Visualizer schema/length/order faults use the same real encrypted peer. Positive
reserved-bit cases use the native frame packer; the transport observer proves a
past-timestamped frame actually arrived instead of being silently dropped by the
reference writer. A later visualization event is also used as an event-consumer
FIFO barrier before asserting the stale frame produced no event. Reduced/duplicate
type echoes, empty requested types and in-place ordering/reset behavior have their
own tests. `FuzzVisualizerStream` validates the production configuration decoder;
stateful media fuzzing checks exact queue-byte sums as well as bounds.

Separate unit/race tests verify image read-model ownership under concurrent readers
and a full notification buffer, availability changes during a partially received
image, safe playback-position ranges, and first-state scheduling while clock
estimates improve. Stateful media-message fuzzing checks accounting and memory
bounds across sequences of binary frames.

Artwork cancellation now also uses an actual transmit-boundary probe: the native
reference writer pauses after sending the first part and a same-direction marker.
The test checks unchanged current artwork, then invokes native cancel, replacement
or role removal and verifies the transmitted ordering and final image hash. A late
replacement is deliberately not dropped. Separate fault tests cover malformed
headers/parts/configuration, all-disabled declarations, size bounds and missing
cancel before stream/end. The probe does not replace reference packers/queue logic
or claim concurrent-audio/display quality. See [the artwork clause audit](rc1-audit.md#artwork-role)
for encoded-byte resource limits and optional features not implemented by the SDK.

The clock suite additionally runs frozen reference traces in every normal test
matrix job, native C++ comparisons in their own Linux/Windows/macOS CI jobs, and
Linux sanitizer checks. The finite oracle suite does not replace hardware timing
measurements. NTP integer arithmetic is fuzzed independently against `math/big`.

CI defines Go minimum/pinned-version jobs and reference E2E on Linux, Windows and
macOS, bounded Linux fuzzing, and an additional native-backend Linux build/test.
The official-harness adapter has a separate Linux CI job.
Managed test tasks record actual machine-readable execution evidence. The final
CI traceability job collects it even after failures; see [the catalog/report
workflow and its explicit limits](traceability.md). Run the full gate with
`mise run --jobs 1 check:full` when sharing loopback port resources locally.
Project-specific outputs live in [the var tree](generated-files.md), the only
entry in the root `.gitignore`. System temporary paths and shared tool caches
retain their defaults; ordinary development tasks do not redirect them.
Only Linux execution has been validated in the local implementation environment.
