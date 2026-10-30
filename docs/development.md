# Development

The repository follows cli4g's library-first layout: public API at the root,
focused public subpackages, private `internal` implementations, independent
`_examples` modules, docs, mise tasks/locks, and GitHub Actions.

**SDK** includes PCM16 Source, static/dynamic PIN pairing, host-managed
player control and purego native backends. These API opt-ins retain their explicit
capture, pairing and host-control requirements. Go **1.27.3 or newer** is required.
Concrete audio backends use root packages `audio/alsa`,
`audio/pipewire`, `audio/wasapi`, `audio/coreaudio` and `audio/noop`; the optional
`audio/native` selector and internal Go tools also belong to the root module.
Each real example has its own `go.mod`/`go.sum`. Oto/PortAudio are no longer
current backends. Historical release tags and recorded results are not rewritten; the pinned RC1 baseline remains unchanged.

Production SDK/backend builds target `CGO_ENABLED=0`, using purego/system dynamic
libraries without audio development headers or a C compiler. Runtime libraries,
services and permissions are still required when opening a device. OS-specific
implementation files are selected at compile time, not by a runtime GOOS switch.

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
a container runtime or access to the developer's LAN. The independent server
remains the locked, unmodified `aiosendspin[server]==10.2.1`.

| Command | Purpose |
| --- | --- |
| `mise run format` | Format and tidy the root and both example modules; regenerate audit tables |
| `mise run format:check` | Show formatting differences without rewriting files |
| `mise run lint` | Aggregate of lint:go, lint:actions, lint:examples and lint:audit |
| `mise run lint:go` | SDK golangci-lint (including govet), formatting, module files and tagged sources |
| `mise run lint:actions` | GitHub Actions workflow checks |
| `mise run lint:examples` | Independent examples' lint, formatting and module files |
| `mise run lint:audit` | Validate machine-readable requirements, actual test links and generated audit |
| `mise run format:audit` | Regenerate the human clause tables from their source catalog |
| `mise run audit:report` | Generate var/audit/report.md and report.json from available evidence |
| `mise run audit:verify-spec` | Explicit network verification of pinned source/paragraph fingerprints |
| `mise run audit:check-conformance` | Strict, currently blocked normative/hardware conformance gate |
| `mise run lint:native` | Lint purego backends, optional selector and native examples without audio development headers |
| `mise run test` | Unit/integration tests with race detection |
| `mise run test:tasks` | File-task discovery/metadata and shell orchestration using fake hardware only |
| `mise run test:loopback` | Internal offline WAV/reference/analysis tool tests |
| `mise run test:examples` | Test example selection without CGO or audio hardware |
| `mise run test:native` | Aggregate desktop-audio, ALSA and example software checks; no hardware opt-in |
| `mise run test:desktop-audio` | Record current root-backend ABI/clock/callback software evidence |
| `mise run test:desktop-audio:stub` | CGO-free root audio tests; no real-device opt-in |
| `mise run test:desktop-audio:race` | Optional race-instrumented audio tests; CGO is for Go's race runtime |
| `mise run test:alsa` | Record purego ALSA null-PCM/fake-device software evidence |
| `mise run build:example` | Build the native player under var/bin/native without CGO |
| `mise run test:desktop-audio:e2e` / `test:alsa:e2e:optin` | Tagged native scenarios; visibly SKIP before device operations without explicit device ENV |
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
| `mise run check:full` | Core, CGO-free examples and independent-reference gates; additional native backend profiles/device checks remain separate |

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

The root module archive includes backends, selector and internal Go tools.
The local archive test selects Git-versioned files only, explicitly excludes
`_examples`, `internal/e2e` and `internal/conformancetest`, and rejects tracked
`var` files. It does not read ignored/untracked generated data. Neither the Python
harnesses nor `var` need nested module markers. Root `go test ./...` includes the
selector and offline tool; each example is tested in its own module.
See [module layout](module-layout.md). E2E execution needs a complete checkout. Unit tests do not
require Python, a container, multicast or a physical audio device. ALSA native
software tests can load system libasound with a private discard-only null PCM;
that is not hardware capture/playback. Race detection requires a C compiler for
Go's instrumentation, not for production backend code.

Root audio tests cover bounded queues, cancellation, loading failures, callback
ABI and clock/lifecycle checks. The selector is tested separately without hardware.
Linux auto selection prefers usable PipeWire and then ALSA if PipeWire is unusable;
explicit backend or library-path failure is an error without fallback. Noop is an
explicit paced discard output, never a fallback or a fabricated input.

See [the native audio test contract](native-audio-tests.md) for current commands,
private PipeWire/null-PCM/software-only scenarios and explicit marker-loopback or
independent-server device opt-ins. Tests requiring real devices must skip before
library/server/output/capture operations when the matching explicit ENV is absent;
those skips do not count as hardware validation. Never run device/mixer-changing
legacy appliance scripts as part of an ordinary software check. The separately
recorded ALSA player E2E through WSL2 to a physical Creative PLAY! 3 is documented in
[platform validation](platform-validation.md). That playback result does not
measure physical synchronization or input timing, and is not Windows/macOS hardware
acceptance. Historical silence-only smoke results remain historical and cannot
validate replacement backends.

## Lint and formatting

`golangci-lint` **2.15.0** is pinned in `mise.toml` with verified artifacts for
Linux amd64/arm64, macOS amd64/arm64 and Windows amd64 in `mise.lock`.
`.golangci.yaml` follows cli4g's v2 configuration: gofmt plus default correctness
linters, asasalint, asciicheck, bidichk, misspell, copyloopvar, exptostd and importas.
Formatting invokes `golangci-lint fmt` directly, including build-tagged sources.
Go does not recursively discover underscore-prefixed directories such as
`_examples`; the `go.mod` ignore directives also exclude `_examples` and `var`
from wildcard discovery. Examples therefore run format/lint/test/build commands
in their own modules, for example `go -C _examples/player test ./...` and
`go -C _examples/player run . -h`. There is no custom formatting wrapper.

`lint` aggregates `lint:go`, `lint:actions`, `lint:examples` and `lint:audit`.
`lint:go` has separate formatting, module and tagged-source checks; govet runs
inside golangci-lint, not through a redundant `go vet` command.
`lint:examples` and `format:examples` explicitly include the player and external-
control examples. `format:check` reuses formatting tasks. Root `format:go` covers
all native packages and `internal/loopbackcmd`; dependency tidying runs separately
for the root and each example module. The player is not a root Go tool. Its
logging provider dependency is application-owned, not required by the SDK root.
Both examples require SDK with a local `replace` to `../..` for checkout
development. Published supplies these APIs without that replacement;
no separate native-module requirement is needed.

## MISE file tasks

Executable scripts under `mise-tasks/` are real file tasks: task names follow
the directory layout and metadata lives in `#MISE` headers. For example,
`mise-tasks/test/network.sh` defines `test:network`, and
`mise-tasks/test/alsa/e2e/_default.sh` defines `test:alsa:e2e`.
There are no duplicate TOML wrappers. Shared implementation code belongs under
`scripts/audio/`, its regression tests under `scripts/tests/`, not in the task
discovery directory. `test:tasks` verifies discovery without running live audio.
Python bytecode stays under `var/python/pycache`. `prepare.sh`/`prepare.ps1`
remain the platform-specific implementations of the single `prepare` task.

`lint` checks ordinary and e2e/mdns_e2e/timefilter_reference tagged packages without
executing them or requiring a LAN/container/audio device. Native example/backend
lint uses the current purego packages; production examples use native/noop rather
than the retired backend variants. No audio development headers are needed.
Dependency resolution during lint is readonly. `.idea`, var and Python
environments are excluded; user IDE files are not rewritten.

The narrow exceptions document actual contracts: RC1's `user_cancelled` spelling
cannot be Americanized, and explicitly annotated cleanup error-channel joins must
not replace the primary cancellation/failure. Other cleanup return values are
handled or explicitly discarded; correctness linters are not globally disabled.
The workflow configures ordinary lint on Linux/Windows/macOS and separate purego
native/backend checks. The [verified implementation CI run](https://github.com/echocat/sendspin-player-go/actions/runs/41034307574)
passed all **27/27 jobs** at revision `084d704`. That execution evidence does not
assert a pass for a later release commit.

## E2E boundaries

The [partial RC1 clause audit](rc1-audit.md) records which requirements each test
actually supports. Keep normative review, independent interoperability, deliberately
injected peer faults, and physical synchronization measurements distinct.

`internal/e2e/server.py` is a thin scenario driver around the **unmodified** Python
server. Its strict mode rejects legacy/nonconforming client messages. It uses
real loopback WebSockets, real Noise, real codec encoders, automatic PSK pairing,
and temporary credentials. Normal paired scenarios use real pairing/trust. Separate
explicit unpaired-policy scenarios deliberately select Sentinel/unpaired access to
test rejection and operator approval; they are not a shortcut around the paired flow.

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

The [Source extension](source-input.md) uses deterministic fake Inputs
with the unchanged server's native source role/decoder. It tests PCM16 upstream,
ADC-time mapping including drift, lifecycle ordering, duplicate/pending starts,
availability, role loss and reconnect, including explicit unpaired **source**
approval distinct from playback approval. Source role plus explicit Input is
required; configuration, activation and reconnect alone never open capture. These
tests do not measure native ADC timing or route captured input to local Output.

[Host-managed player-control tests](external-player-control.md) exercise fake host
targets, independent readback, a bounded serialized FIFO without coalescing,
cancellation/generation invalidation and immutable unity gain. They
do not implement/test CEC, ALSA mixer access or HDMI/appliance policy. Non-nil
PlayerControl, including fixed/no-controls, never pushes stored software values to
hardware or falls back to software attenuation. Runtime SDK/backend logging uses
only the base slf4g facade and optional injected logger; provider installation is
the host's choice, never a capture/control side effect.

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

The clock suite includes frozen reference traces in normal tests. The workflow
configures native C++ comparisons in Linux/Windows/macOS jobs and Linux sanitizer
checks. The finite oracle suite does not replace hardware timing
measurements. NTP integer arithmetic is fuzzed independently against `math/big`.

CI defines pinned/stable Go jobs and reference E2E on Linux, Windows and macOS,
bounded Linux fuzzing, and a separate Linux official-harness job. The desktop
backend matrix covers Linux/macOS/Windows on **amd64 and arm64**, with CGO-free
ABI/software checks and visible skips for non-opted-in device scenarios. All
**27/27 jobs** passed for implementation revision `084d704` in the linked run;
the release commit still needs its own validation. Local test and cross-build
results are limited to their exact source, host, target and command; a cross-build
is not native execution, and earlier artifacts cannot satisfy a changed source
fingerprint.
Managed test tasks record actual machine-readable execution evidence. The final
CI traceability job collects it even after failures; see [the catalog/report
workflow and its explicit limits](traceability.md). Run the full gate with
`mise run --jobs 1 check:full` when sharing loopback port resources locally.
Project-specific outputs live in [the var tree](generated-files.md), the only
entry in the root `.gitignore`. System temporary paths and shared tool caches
retain their defaults; ordinary development tasks do not redirect them.
Hosted native Windows/macOS software results do not establish hardware behavior.
Physical timing, native Input hardware acceptance and independent cryptographic
assurance remain open; `audit:check-conformance` intentionally stays blocked.
