# Native audio test contract (unreleased)

All concrete backends are root packages: `audio/alsa`, `audio/pipewire`,
`audio/coreaudio`, `audio/wasapi` and the `audio/native` selector share one module.
Production builds use purego/system dynamic libraries with `CGO_ENABLED=0`; no
audio development headers or C compiler are required. The optional race task
uses CGO for Go's race runtime, not for backend implementation.

## Software validation, no real devices

```text
mise run test:desktop-audio
mise run test:alsa
mise run test:desktop-audio:stub
mise run test:desktop-audio:race
mise run test:desktop-audio:e2e
mise run test:alsa:e2e:optin
```

The ordinary suites exercise library load failure, native clock/callback ABI,
bounded queue ownership, cancellation and lifecycle failures. Linux ALSA tests
use a private discard-only null PCM with the system libasound, not a hardware
device. General root tests may skip this ABI check when libasound is absent;
the dedicated `alsa` audit runner sets `SENDSPIN_ALSA_REQUIRE_LIBRARY=1` so its
installed runtime must actually be exercised. This flag opens no hardware.
Never override `LD_LIBRARY_PATH` with an incompatible Nix libasound/glibc
combination. Constructor/import does not open output or capture.

Backend E2E files require `-tags=e2e` and the matching OS and architecture
(amd64/arm64). Without explicit device ENV they visibly **SKIP before any
library, audio server, output or capture operation**. A successful test command
containing these skips is not hardware validation. Backend E2E audit profiles
report skipped suites as `skip`, not `pass`. No recordings are uploaded by CI.

The desktop GitHub matrix covers Linux, Windows and macOS on amd64/arm64.
Hosted runners have no device opt-in: ABI tests run, physical tests skip. The
root SDK independent-server matrix continues using its virtual clock output;
that is protocol/software evidence, never native playback evidence.

Linux additionally supports two explicit private-software-graph fixtures:

```text
mise run test:pipewire:isolated
mise run test:pipewire:e2e:isolated
mise run test:alsa:e2e:virtual
```

The first runs real PipeWire lifecycle/server-loss calls. The second runs the
pinned `aiosendspin[server]==10.2.1` localhost encrypted scenario through the
real PipeWire library/graph. Both start only their own private null-sink daemon:
no host server, hardware, session manager, capture, Bluetooth or network audio
modules. The independent-server graph test uses four short three-second phases.
The third task explicitly uses a virtual output; it is not an ALSA native test.
None measures a physical clock or analog silence. Private graph configuration
does not change the user's audio service, mixer, defaults or routing.

## Native output + input marker loopback (explicit opt-in only)

Run `TestNativeLoopback` in the relevant package with `-tags=e2e`. Before running,
set the matching `SENDSPIN_<BACKEND>_LOOPBACK_DEVICE` to an explicit device ID.
No test chooses a default microphone or changes a gain/mixer setting.

| Backend | `<BACKEND>` | Common device and capture semantics |
| --- | --- | --- |
| ALSA | `ALSA` | Explicit duplex PCM, e.g. a verified `hw:CARD=...,DEV=...`; actual ADC/physical cable |
| PipeWire | `PIPEWIRE` | Explicit sink node; `Input.Loopback=true` captures its monitor, not a microphone |
| WASAPI | `WASAPI` | Explicit render endpoint ID; `Input.Loopback=true` captures OS render loopback |
| CoreAudio | `COREAUDIO` | Explicit duplex device UID; actual ADC/physical cable, not an output monitor |

`SENDSPIN_<BACKEND>_LOOPBACK_INPUT_DEVICE` optionally selects a separate explicit
input ID; otherwise **the explicit common device ID** is reused, never the
system default input. For PipeWire/WASAPI it still names a sink/render endpoint,
not a microphone. For CoreAudio/ALSA a distinct ADC ID may be needed. Confirm
wiring/levels independently; peak 0.01 is not an electrical safety guarantee.

The fixture builds `internal/loopbackcmd` **before opening any device**, generates
a three-second 48kHz/stereo left-only reference at peak 0.01, captures five to
six seconds through the native Input API and requires the offline analyzer to
find the expected markers. Zero-only output/capture smoke is not a substitute.
The capture and reference WAVs live in private temporary files, are never
logged/uploaded and are deleted on cleanup. Unexpected/discontinuous input,
missing markers or inconclusive alignment fail rather than becoming a pass.

The result proves marker routing, not absolute output latency. Capture timing,
ADC/DAC and unknown calibration/state remain undetermined. PipeWire/WASAPI
software monitors are not analog loopback or physical synchronization evidence.

## Independent-server native playback scenario

`TestReferenceServerPlaybackScenario` is available in each backend. The shared
implementation is `audio/internal/audiotest`, built only with `e2e`, with a lazy
`OutputFactory`. Set:

- `SENDSPIN_<BACKEND>_E2E_PLAYBACK=1`
- `SENDSPIN_<BACKEND>_E2E_DEVICE=<explicit output ID>`
- `SENDSPIN_<BACKEND>_E2E_ROOT=<absolute checkout>`
- `SENDSPIN_<BACKEND>_E2E_ARTIFACT_DIR=<absolute new private directory>`
- `SENDSPIN_<BACKEND>_REFERENCE_FILE=<absolute generated weak reference.wav>`

Prepare the pinned Python environment first (`uv sync --locked --project
internal/e2e` with `UV_PROJECT_ENVIRONMENT=<checkout>/var/python/e2e`). The
scenario runs PCM 60s, stop/pause 2s, resume PCM 30s, disconnect/authenticated
reconnect PCM 30s, FLAC 60s and final stop. It checks decoded/submitted nonzero
frames and output cleanup, not physical audibility, latency or stop-silence.
It never opens capture. The private receipt explicitly records virtual output
and private software graph state. Native failure never falls back to a fake.

The existing ALSA test name and `SENDSPIN_ALSA_*` variables are retained.
Scripts must now pass `-tags=e2e` and `SENDSPIN_ALSA_E2E_DEVICE`; there is no
hard-coded PLAY! device. `SENDSPIN_<BACKEND>_E2E_VIRTUAL=1` is a separate, mutually
exclusive software option using short phases, not native evidence.

## Source-only native Input E2E

Each backend also provides `TestReferenceServerNativeInputScenario`, built only
with `-tags=e2e` on its matching OS/amd64/arm64. It requires a **separate explicit
input consent/device**: `SENDSPIN_<BACKEND>_SOURCE_E2E_DEVICE`. Without it the test
skips before any library, server or capture operation. Playback/loopback ENV is
never reused as source-input consent. In particular, WASAPI requires a **capture
endpoint ID**, and PipeWire uses a capture node with `Loopback=false`.

The helper accepts `SENDSPIN_<BACKEND>_SOURCE_E2E_ROOT` and
`SENDSPIN_<BACKEND>_SOURCE_E2E_ARTIFACT_DIR`; if absent, it reuses the corresponding
`SENDSPIN_<BACKEND>_E2E_ROOT` / `_E2E_ARTIFACT_DIR`. Both paths must be absolute.
It creates a fresh private `source-e2e-*` subdirectory, long-term credentials and
`observe_source=true` configuration for the same pinned unmodified independent
10.2.1 server and its source decoder. No weak-reference WAV is needed.

The client advertises **SourceRole only**, with native `Config.Input` and a
48kHz/2-channel `SourceFormat`; it has no Player role, Output or local monitoring.
After server-observed available/ready source state, explicit `source_start`
commands authorize two short capture periods. Each `source_stop` must produce
`client-stream/end`, decoder drain and native input close. Idle checks forbid
further reads/automatic reopening without another explicit start. The same
explicit input device is bound on both starts; there is no noop/default fallback.

Checks cover Open/Read/Close counts, sample/frame geometry, PCM16 wire format,
independent-decoder byte counts, FIFO start/audio/end ordering and broad local/
server-clock-domain sanity. Unknown physical microphone/line-in content is
**not** a signal-content acceptance criterion. Two wire channels do not establish
physical stereo routing, audibility, calibrated ADC/DAC latency or synchronization.
Samples are never retained/logged/hashed by the Go tap, no WAV is written, and
decoder hashes are omitted from the receipt. Native receipts identify
`capture_opened:true`, `virtual:false`, `output_opened:false` and
`physical_conformance:false` after a successful capture.

The existing hosted E2E matrix includes these entrypoints but sets no source
device ENV; native Source tests therefore visibly skip. Backend E2E audit profiles
require the new native Input entrypoint as well as output/marker tests; an absent
or skipped input run cannot become complete backend hardware evidence.

For hardware-free helper validation only, set
`SENDSPIN_AUDIOTEST_SOURCE_E2E_FAKE=1` and run
`go test -tags=e2e -run '^TestReferenceServerFakeInputScenario$' ./audio/internal/audiotest`
after preparing the pinned Python environment. This uses **fake input**, never a
native device; its receipt is explicitly `virtual:true`, `capture_opened:false`.
`mise run test:desktop-audio:source:fake` prepares the pinned environment and
runs this software-only check; CI runs it separately on Linux amd64, never as a
replacement for a skipped native Source test.
The root SDK's separate Source fake-input E2Es remain independent software
coverage. No native private-graph Source/Input measurement is claimed.

## Audit migration boundary

Oto and PortAudio jobs/tasks/executable suites are retired. Historical `var/`
artifacts remain unchanged and cannot satisfy current source fingerprints.
The `native` report profile now requires `desktop-audio` plus `examples`, not
PortAudio. `alsa`, `desktop-audio`, `alsa-e2e` and `desktop-audio-e2e` stay distinct.
Native bridge/render test names are retained under `audio/internal/...`.

The frozen RC1 catalog must be reviewed when paths/test IDs move. Merely moving
or replacing a backend does not promote a partial assessment, close source
gaps, discard atom references or certify hardware. Until its references are
migrated explicitly, the real-catalog check must fail honestly.
