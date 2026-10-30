# Direct desktop outputs (v0.2.0)

The root packages `audio/pipewire`, `audio/wasapi` and `audio/coreaudio`
provide direct **PipeWire**, **WASAPI** and **CoreAudio** playback. They share one
bounded, tested callback queue/lifecycle implementation instead of three subtly
different copies. It does not import Oto, PortAudio or a concrete logging provider.
The root package `audio/native` selects a backend. Production builds work
with `CGO_ENABLED=0`; native libraries are loaded at runtime. E2E tests alone
use the explicit `e2e` build tag. See the [test/CI contract](native-audio-tests.md).

SDK **v0.2.0** includes these backends and `audio/alsa`; v0.1.1 does not contain
them. For checkout development, a local consumer can replace only the root:

```text
go mod edit -replace github.com/echocat/sendspin-player-go=/absolute/path/to/checkout
```

All backend and selector import paths belong to that one module/version; a
v0.2.0 consumer does not need a local replacement or separate backend modules.
The independent example modules require SDK v0.2.0 and replace the root with
`../..` for checkout development; neither needs a separate `audio/native` module
requirement. From the checkout root, use
`go -C _examples/player run . -h` or `mise run build:example`.
`ampanion` is not modified by this work.

## Use

```go
import "github.com/echocat/sendspin-player-go/audio/native"

// In sendspin.Config:
Output: native.Output{},
```

`native.Output` selects WASAPI on Windows and CoreAudio on macOS. Linux auto
selection prefers usable PipeWire and otherwise ALSA; an explicitly missing
`SharedLibPath` is an error, never fallback. Noop requires explicit selection.
Individual outputs are imported as `audio/pipewire`, `audio/wasapi`,
`audio/coreaudio` and `audio/alsa`. Unsupported platforms return unavailable.

All outputs accept:

- `SharedLibPath`: exact dynamic-library selection when nonempty. A load or
  symbol failure is returned without trying another library.
- `Device`: empty for the default at Open; otherwise a PipeWire node name/serial,
  Windows IMMDevice endpoint ID, or macOS device UID. These identifiers are
  backend-specific, not interchangeable with ALSA `hw:` names.
- `BufferDuration`: SDK queue capacity, default 100ms, allowed 10..500ms. This is
  not measured hardware latency and does not reconfigure the global device buffer.
  It must fit a native render block; an undersized queue fails rather than
  producing inevitable repeated underruns. Increase it for unusually large
  driver/graph quanta.
- `StartupTimeout`: default 2s, allowed 10ms..10s, waiting for genuine native
  timing after starting playback. An initial missing timestamp emits zeros only.
- `MaxTimingWindow`: default 2ms, allowed (0,50ms], bounding the native/Go clock
  correlation call window, **not** physical device accuracy.
- `Logger`: optional slf4g base-facade logger. Applications choose the sink. The
  SDK logs lifecycle/backend names, not device identifiers, raw errors or audio.

Construction/import does not open anything. `Open` configures an inactive
playback stream; `Next` or the first nonempty `Write` starts it with silence.
Capture is a separate explicit `Input.Open`/`Read` API, never started by an output
constructor/import. There are **no mixer, global default-device, global sample-rate,
audio-service or network changes**. Ordinary tests do not open devices; explicitly
opted-in native E2E/loopback tests may play/record. Native library error
output is not intercepted through process-global error handlers.

## Platforms and timing

| Platform | Native API/build | Timestamp used |
| --- | --- | --- |
| Linux desktop / Raspberry Pi OS with PipeWire | purego; PipeWire >=0.3.50 runtime library and existing session routing | `pw_stream_get_time_n`: graph delay in graph ticks plus queued stream frames and resampler frames, anchored to MONOTONIC `pw_time.now` |
| Windows | purego; system ole32/WASAPI DLLs | `IAudioClock::GetPosition` and `GetFrequency`, correlated QPC in 100ns units; submitted stream frames minus device-clock position |
| macOS | purego; system AudioUnit/CoreAudio/CoreFoundation frameworks | AUHAL render callback host time plus reported output device latency at its nominal rate; mach absolute time correlated to Go |

Only runtime libraries are required, not development headers. PipeWire must
already be running with suitable routing;
the SDK never starts/stops the user's server or session manager. Minimal/headless
Pi installations without PipeWire should use the separate [direct ALSA output](linux-alsa.md).
The server may resample or route streams according to its existing policy. The
backend accounts for stream resampler buffering instead of treating graph ticks
and application frames as the same unit.
Native render demand comes from `pw_buffer.requested`, SPA RateMatch, or an exact
conversion of SPA Position's graph duration (for older equal-rate streams).
Allocation capacity is never mistaken for the processing quantum; missing or
fractional demand without rate-matching metadata is rejected, not guessed.

No pkg-config, `CGO_CFLAGS_ALLOW` override or C compiler is needed.

WASAPI uses event-driven shared mode with native rate conversion as needed. It
does not take exclusive ownership or alter the endpoint format. Standard Windows
layouts with 1, 2, 4, 6 or 8 channels are supported; SDK examples use stereo.
All COM interfaces and initialization/teardown remain on one native worker thread.
Zero startup positions and `S_FALSE` clock reads produce silence, not guessed
media timestamps. Backwards/underrun clock timelines fail rather than being
silently reanchored. No maximum `GetStreamLatency` estimate is double-counted
on top of the IAudioClock speaker-position clock.

CoreAudio uses AUHAL with input explicitly disabled. It sets only the unit's
client format; native output conversion is used where supported, and unsupported
formats return errors. It never writes nominal device rate or buffer size.
The callback timestamp already includes HAL scheduling; the backend adds reported
device latency, not another guessed safety/buffer offset. Device-alive, nominal
rate and latency changes are checked before submissions and invalidate the open
device instead of leaving a stale cached correlation. It does not migrate to a
different default device behind the application's back.

## Queue, cancellation and stop

The shared queue copies caller-owned samples, validates complete finite float32
frames and clips to [-1,1]. Callback buffers are native-owned and never retained.
Callback work has bounded sample copies and nonblocking notification; no network,
logging, native lifecycle operations or sample-buffer allocations occur there.
The Go mutex/runtime still do **not** promise hard real-time execution.

`Next` includes both native submitted output and SDK queue contents. Missing,
stale or invalid clocks return `ErrTiming`; native failures return an error or
`ErrDriver`. Native callbacks release no media after a timing failure. If an
underrun has overtaken a `Next` reservation, `Write` returns `ErrClockReset`
instead of silently moving the scheduled samples. The shared SDK renderer owns
ordinary drift corrections and resynchronization decisions.
The v0.2.0 root renderer also understands the optional `AudioTimelineReset`
error capability: it remaps from the original source timestamp with a bounded
retry budget. Empty/rejected writes do not restart an already-running pipeline;
partially accepted writes require a native flush before stale-frame removal.
This recovery is new SDK behavior, not present in the published v0.1.1 renderer.
The reservation covers consecutive writes (including alignment silence followed
by media) until another `Next` or `Flush`; an idle gap must not silently rebase
the second half of a scheduled submission. Blocked writers also poll native
health and stale-callback state, without using those timers as an audio clock.

`Flush` disables media callbacks, drops the SDK ring, stops/resets the native
stream, then conservatively waits through the last reported submitted tail. It
does not drain queued media. A canceled tail wait remains pending for retry or
Close; restart is lazy, with fresh native timing. `Close` wakes blocked writers,
joins callbacks before deleting their bridge token and is idempotent, including
its error result. Native API calls and arbitrary logging sinks may still block;
Go context cancellation cannot forcibly preempt C or safely destroy an active
native callback. Driver/server/FIFO latency reports remain external dependencies.
Native teardown failures are reported, not advertised as safe forced disposal
of an active native callback.

## Validation boundaries

```text
mise run lint:desktop-audio
mise run test:desktop-audio
mise run test:desktop-audio:stub
```

The normal suite loads the host library without CGO, exercises native
clock/callback ABI and timing transforms (including overflow and bad domains), and
tests queue ownership, bounded backpressure, cancellation, underruns and stop
faults with fake drivers. It **does not open an audio device or connect to an
audio server**. Platform evidence has a separate `desktop-audio` audit profile;
it cannot fulfill hardware requirements or inherit another platform's success.
Oto/PortAudio suites are retired; historical artifacts remain historical.
See [native audio tests](native-audio-tests.md) for the platform/architecture CI
matrix, marker loopback opt-ins and independent-server scenarios.

`mise run test:pipewire:isolated` additionally starts a **private** PipeWire server
with only a timer-driven null sink, private configuration and short private Unix
sockets, no session manager and no hardware/network/capture modules. It runs
three real native open/write-silence/flush/restart/close cycles and tests server
loss while a writer is blocked. This is real PipeWire API/lifecycle evidence,
**not** a device-clock test. Configurations/logs stay in
`var/platform-validation/native-desktop/pipewire`; only the fixture's own short
runtime socket directory is removed after teardown. Ordinary native audits
explicitly disable this opt-in, keeping their no-server scope distinct.
Its 1024-frame graph quantum is a fixture setting, not a recommended hardware
latency. A shared CI host can overtake a `Next` reservation; the lifecycle harness
records and bounds explicit flush/rescheduling of **zeros only** for that precise
`ErrClockReset`. Other failures remain fatal. Production timing guards and the
exact underrun tests are unchanged; this fixture does not prove uninterrupted
playback or timing accuracy under host scheduling pressure.

There is no physical +/-1ms synchronization, audibility, rate-window or downstream
silence certificate. Actual Windows/macOS outputs, real PipeWire hardware routing,
USB FIFOs, device hotplug and Raspberry Pi execution require separate device
acceptance. An output-to-line-in loopback measures output plus input latency;
absolute output accuracy additionally needs capture calibration/common reference.
