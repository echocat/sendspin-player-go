# Native platform and physical-output validation

Runtime execution, compilation, virtual audio and physical measurements are
different evidence classes. The normal native suite checks callback code without
opening a sound device. Cross-builds are not native Windows/macOS execution.

## Current native validation

Use the root SDK native backend unit tests and opt-in E2E routes documented in
the [native audio test contract](native-audio-tests.md). The standalone
`_examples/audio-probe` module and its Oto/PortAudio validation tasks are retired;
there is no separate probe to run.

Ordinary native tests use software fixtures rather than real devices. Backend
E2E tests require `-tags=e2e` and explicit, matching device consent; without it
they skip before library, server, output or capture operations. A successful
command containing skips is not hardware validation. Playback, marker loopback
and source input have separate opt-in requirements in the contract; playback
consent never authorizes source capture. No default microphone, mixer or host
audio routing change is part of these routes.

### Recorded native ALSA player E2E

The user ran the SDK's native ALSA player through the unchanged
`aiosendspin[server]==10.2.1` peer on a Creative PLAY! 3 (`S3`, USB `041e:324d`)
under WSL2 at revision `9b59888c05cc25906d7c8a21f6e9b0de1e753c0e`. The retained
receipt is `var/platform-validation/play3-low-level/run-8LBYmfHE/e2e-report.json`;
its reviewed summary is the adjacent `RESULT.md`. These private artifacts are
not release assets; do not upload the test credentials or captured media.

The 184.53-second functional scenario passed: PCM playback, stop/pause/resume,
deliberate disconnect/reconnect, FLAC playback and final teardown. The receipt
records 5,422 writes, 8,639,232 submitted frames, four flushes, two opens/two
closes and zero unexpected output errors. Capture was never opened; the result
was neither virtual output nor a private software graph.

This is playback evidence for that exact earlier revision, not a hardware rerun
of the v0.2.0 release commit. It does not establish absolute latency, calibrated
synchronization/rate, analog stop/flush silence, click-free output, native Source
capture, Windows/macOS hardware or host volume/mixer/CEC behavior. Frozen
conformance assessments are unchanged.

## Historical silence-only probe evidence (retired)

The former standalone probe opened output only with explicit `-run-silence`
consent. It did not open input or monitor capture, play nonzero samples, change
volume or reconfigure the audio server. Its pass covered only same-format
open/write/query/flush/cancel/close/reopen, not physical timing or downstream stop.

The historical PortAudio task required a native compiler and development
libraries, using automatic `cgo` constraints. The Oto task explicitly opted into
estimated timing/software-only flush. Default output selection was preserved;
a missing device/toolchain was **blocked**, never a physical-test pass.
Existing `var/platform-validation`, `var/oto-output-smoke` and related historical
artifacts remain unchanged. They are not current native-backend validation or
normative measurement evidence and cannot satisfy current source fingerprints.

### Recorded WSLg with Nix PortAudio setup

The retired WSLg task supplied the matching Nix ALSA Pulse plugin and created an
output-only private ALSA route under `var/platform-validation/wslg`.
`ALSA_PLUGIN_DIR`, `ALSA_CONFIG_PATH` and `PULSE_SERVER` applied only to that
process tree. It required no `sudo`, Fedora package installation, `~/.asoundrc`,
`/etc` change, microphone or default host-device change. Its first run could fetch
Nix dependencies. This describes the historical setup, not a current invocation.

The historical result path was `var/platform-validation/wslg/portaudio.json`.
Device discovery and silence-only lifecycle over WSLg did not make virtual
Pulse/ALSA timestamps a physical Linux DAC or a loopback accuracy certificate.
The probe's start/restart waited for actual callback timing, with cancellation/
startup bounds, rather than failing before the first asynchronous callback or
inventing a timestamp.

## Recorded execution-target boundary

The recorded Linux validation environment was WSL2 with WSLg/RDP PulseAudio. Its
audio server could support virtual-stream smoke tests but was not a directly
exposed physical Linux DAC. Merely finding RDPSink or querying its latency cannot
establish RC1 output accuracy, audibility, hardware speed or physical flush
behavior.

Windows native tooling can be invoked separately. WSL UNC file locking is not
supported by the available Windows Go path, so native tests need a fresh source
snapshot on the native development drive. Record its archive/file hashes, native
toolchain, exact commands, exit statuses and logs. Do not overwrite a user checkout
or globally redirect Go/uv caches or temporary directories. A no-CGO/no-race run
must stay separately labelled and must not replace race-enabled evidence slots.

Executables themselves can remain under the repository's `var` directory and run
directly through WSL interop. The source snapshot is needed for Windows Go file
locking/source-aware tests, not for executing a Windows `.exe`. This also allowed
the former silence probe to be cross-compiled into `var` and executed there
natively; that historical route is retired.

The first actual Windows run exposed coarse runtime-clock assumptions: fast
loopback RTTs could collapse to zero and startup would not converge. Protocol
transmit/arrival and scheduling now share a pure-Go Windows QPC-backed process
clock, with exact saturating counter conversion and nondecreasing concurrent
reads. A low-resolution/runtime fallback is explicitly identifiable. Invalid RTTs
are still rejected; startup retries remain paced by the actor ticker instead of
entering the ten-second converged-state pause. Neither QPC nor its local runtime
correlation proves DAC accuracy. Unit tests also avoid treating a few microseconds
of synthetic test-time advance as guaranteed progress of the Windows runtime clock.

Windows CI also exposed concurrent credential-file reads blocking server-driven
unpair persistence. FileStore readers now share delete access; same-directory
replacement uses Windows POSIX rename semantics so open readers retain their
complete old snapshot and new readers see the replacement. Filesystems without
that operation fall back to ordinary rename, preserving its sharing limitations;
errors are not retried or reported as successful persistence. Regression tests
hold a reader open during replacement and the independent unpair exchange.

The local workstation has no native macOS target or physical loopback fixture.
The separately authorized hosted CI matrix executes native Go tests, independent
E2E and C++ reference comparisons on Linux, Windows and macOS; inspect the actual
[workflow results](https://github.com/echocat/sendspin-player-go/actions/workflows/ci.yml)
for the relevant commit. Those jobs do not establish physical audio behavior.
Cross-compilation alone is never runtime evidence. The validation commands do not
commit/push or open microphone/mixed-system-audio capture; repository publication
and remote CI require their own authorization.

## Physical acceptance still requires an explicit setup

Use an identified output, a dedicated loopback/reference capture path and an
agreed safe signal/level, isolated from microphone or other application audio.
Record sample clocks, rates, latency calibration, uncertainty and raw traces.
Measure RC1 steady-state error relative to its time-filter prediction/external
delay definition, sliding 150-ms device speed, startup, rare resynchronizations,
audibility and post-clear output. Virtual streams and callback unit tests cannot
satisfy those measurement obligations.
