# Direct Linux ALSA output (unreleased)

`audio/alsa` is an independent optional Go module. It uses libasound directly,
without Oto or PortAudio. The root SDK remains pure Go and gains no native
dependency. The backend requires Linux, CGO, a C compiler, `pkg-config` and ALSA
development files (`libasound2-dev` on Debian/Ubuntu/Raspberry Pi OS;
`alsa-lib-devel` on Fedora). Native constraints are automatic, not custom tags.
Deployed dynamically linked binaries also need the libasound runtime
(`libasound2`/`libasound2t64` or `alsa-lib`, depending on the distribution).
Other systems and CGO-free builds expose an `ErrUnavailable` stub.
libasound is a system LGPL-2.1-or-later dependency, not vendored SDK source.

This module is **not in v0.1.1 and has no release tag yet**. Work from this
checkout until a release is explicitly prepared. For a local consumer use an
explicit `replace github.com/echocat/sendspin-player-go/audio/alsa => /path/to/checkout/audio/alsa`.
Its root SDK requirement is the published v0.1.1, not a nonexistent development
version. Existing published tags are unchanged.

## Integration

```go
import "github.com/echocat/sendspin-player-go/audio/alsa"

// In sendspin.Config:
Output: alsa.Output{Device: "hw:CARD=Device,DEV=0"},
```

Replace `Device` with your actual card identifier; the example is not a device
discovery result. Prefer stable `CARD=` names over hotplug-sensitive card numbers.
`aplay -l` from ALSA utilities can list hardware playback cards without playing audio.
Construction/import does not open audio. Only `Open` opens a playback PCM;
`Next`/first `Write` starts it with bounded digital silence pre-roll. The package
never opens capture, changes mixer volume, changes the system default, or
reconfigures an audio server. ALSA's own errors may go to stderr; the SDK does
not install process-global ALSA error handlers. Optional `Logger` uses only the
slf4g base facade and omits device names and raw errors; sinks must return promptly.

The existing Oto/PortAudio player example is deliberately unchanged: selecting
ALSA in a consumer does not require those modules or their native libraries.

## Hardware versus desktops

- Direct `hw:` playback is the baseline for a dedicated Raspberry Pi/headless
  player and physical measurements. ALSA must accept the exact sample rate and
  channel count. Native float, S32, S24 (32-bit or packed) and S16 little-endian
  PCM are negotiated in that order; finite float samples are clipped and integer
  conversion is rounded. There is no hidden rate/channel conversion or dither.
- Hardware opening can fail if PipeWire/PulseAudio already owns the PCM. Do not
  stop the desktop server or change global routing automatically. Select an
  available dedicated card, or an appropriate desktop route.
- An empty name selects ALSA `default`. That often resolves to a plugin rather
  than a direct HW PCM. Non-HW routes require **`AllowPluginTiming: true`**. This
  acknowledges unverified downstream timing and flush behavior; it is not a
  certification. Missing plugins, unsupported formats and unavailable servers
  still return errors; there is no silent fallback to another backend.
- ALSA reaches current Linux desktops through their configured plugins, but a
  direct [PipeWire backend](native-desktop-audio.md) is the desktop-native
  complement. The same optional module also provides Windows WASAPI and macOS
  CoreAudio outputs. All are unreleased and physically unmeasured.

## Scheduling and lifecycle

Default requests are a 50ms buffer and a 5ms period. ALSA may negotiate nearby
sizes; `device.(alsa.Device).Info()` reports the actual frame counts/encoding.
The options are bounded hints, not measured hardware latency.

`Next` correlates a single ALSA PCM status snapshot (MONOTONIC timestamp and
reported delay) with the SDK's Go monotonic clock. The call window is bounded
by `MaxTimingWindow` (default 2ms). Missing/invalid timing fails rather than using
a wall-clock/sample-counter prediction. This bounds the clock-correlation call,
**not** driver timestamp accuracy or physical output latency. It does not request
or assert a hardware link-clock timestamp/accuracy report.

Native writes are nonblocking, bounded to negotiated periods and handle partial
frames, EINTR and EAGAIN with cancellable waits. A write-time underrun returns
`ErrClockReset` instead of silently moving already-scheduled samples to a new
timeline. `Next` can rebuild a drained/underrun timeline with silence. Disconnect
and suspend errors propagate; the SDK may reopen on session recovery. No Go
goroutine races native calls to forcibly close a live C handle.
Automatic start is disabled; an empty ring explicitly triggers underrun rather
than replaying old contents. The backend does not enable ALSA automatic silence
insertion to disguise a missed timeline.

`Flush` uses `snd_pcm_drop`, not drain, and resets preparation. Successful stop
waits conservatively through the previously reported/submitted tail, without
playing the discarded ring. A canceled stop retains that tail for a retry/Close;
unknown timing returns an error rather than claiming downstream silence. Close
is idempotent and interrupts userspace retry waits. Broken drivers, arbitrary
plugins, native calls or logger sinks can still block; Go contexts cannot promise
preemption of C code. USB FIFOs and driver-reported delays remain hardware duties.

## Validation now and with the USB loopback card

Hardware-free checks:

```text
mise run lint:alsa
mise run test:alsa
mise run test:alsa:stub
```

The native suite tests the real libasound ABI against ALSA's discard-only `null`
PCM with a private per-handle configuration (host aliases cannot redirect it to
hardware), and uses fake PCMs for timing, partial writes, cancellation and drop/close
faults. It explicitly clears the live-test opt-in. Evidence is retained under
`var/audit/evidence/alsa`; `report --profile alsa` keeps it separate from the
PortAudio/native profile and all physical conformance requirements.

For an **explicit silence-only hardware lifecycle test**, run in `audio/alsa`
after checking the selected card. Example in Nushell:

```nu
with-env {CGO_ENABLED: "1", SENDSPIN_ALSA_SILENCE_DEVICE: "hw:CARD=Device,DEV=0"} {
    mise exec -- go test -count=1 -v -run '^TestHardwareSilenceLifecycle$' .
}
```

This opt-in opens playback only, at 48kHz/stereo, for three short
open/write/flush/reopen/close cycles. Ordinary tests skip it. Silence cannot
prove analog mute/flush, audibility, speed or synchronization.

When the USB output-to-line-in loopback card arrives, first identify its output
and input routes and verify safe levels. Actual nonzero signals and line-in
capture need a separate explicit measurement run; none is enabled here. Measure
startup, sustained drift, underrun/restart and flush/stop residuals. A single
loopback measures **output + input** latency; absolute +/-1ms output accuracy
additionally needs calibrated capture latency or a common-clock reference.
Neither fake-device tests nor the null PCM are physical evidence. ARM/Raspberry
Pi runtime, real desktop routing and actual USB hardware remain to be tested.
