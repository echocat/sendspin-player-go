# Native platform and physical-output validation

Runtime execution, compilation, virtual audio and physical measurements are
different evidence classes. The normal native suite checks callback code without
opening a sound device. Cross-builds are not native Windows/macOS execution.

## Explicit silence-only output probe

The standalone `_examples/audio-probe` module is separate from the player. It never opens input or
monitor capture, plays nonzero samples, changes volume, or reconfigures the audio
server. Opening output requires `-run-silence`. A pass covers only same-format
open/write/query/flush/cancel/close/reopen, not physical timing or downstream stop.

```text
mise run validate:audio:oto
mise run validate:audio:portaudio
```

The PortAudio task needs a native compiler and development libraries; its sources
use only automatic `cgo` constraints. The Oto task explicitly opts into estimated
timing/software-only flush. Default output selection is preserved. A missing
device/toolchain is **blocked**, never a physical-test pass. Results belong under
`var/platform-validation`; these files are not normative measurement evidence.

### WSLg with Nix PortAudio

Run this from the repository in WSL (the command also works from Nushell):

```text
mise run validate:audio:portaudio:wslg
```

This task supplies the matching Nix ALSA Pulse plugin and creates an output-only
private ALSA route under `var/platform-validation/wslg`. `ALSA_PLUGIN_DIR`,
`ALSA_CONFIG_PATH` and `PULSE_SERVER` apply only to that process tree. No `sudo`,
Fedora package installation, `~/.asoundrc`, `/etc` change, microphone or default
host-device change is required. The first run may fetch Nix dependencies.

The result is `var/platform-validation/wslg/portaudio.json`. Device discovery and
silence-only lifecycle can work over WSLg, but virtual Pulse/ALSA timestamps are
not a physical Linux DAC or a loopback accuracy certificate. Start/restart waits
for actual callback timing, with cancellation/startup bounds, instead of failing
before the first asynchronous callback or inventing a timestamp.

## Current execution-target boundary

The available Linux environment is WSL2 with WSLg/RDP PulseAudio. Its audio server
can support virtual-stream smoke tests but is not a directly exposed physical
Linux DAC. Merely finding RDPSink or querying its latency cannot establish RC1
output accuracy, audibility, hardware speed or physical flush behavior.

Windows native tooling can be invoked separately. WSL UNC file locking is not
supported by the available Windows Go path, so native tests need a fresh source
snapshot on the native development drive. Record its archive/file hashes, native
toolchain, exact commands, exit statuses and logs. Do not overwrite a user checkout
or globally redirect Go/uv caches or temporary directories. A no-CGO/no-race run
must stay separately labelled and must not replace race-enabled evidence slots.

Executables themselves can remain under the repository's `var` directory and run
directly through WSL interop. The source snapshot is needed for Windows Go file
locking/source-aware tests, not for executing a Windows `.exe`. The silence probe
can therefore be cross-compiled into `var` and executed there natively.

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
