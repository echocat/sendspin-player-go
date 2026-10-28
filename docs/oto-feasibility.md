# Oto timing / flush feasibility experiment

## Result

**Integration update:** `audio/oto` now ships a separate, explicitly opt-in
best-effort backend using unmodified Oto v3.5.1. The extension below is still only
a local experiment; its timing/flush API is not silently included in that backend.
See [the shipped backends and limitations](usage.md#audio-backend-comparison).

A local Oto extension can obtain **same-stream PulseAudio timing feedback** and
acknowledge an empty, corked server stream without CGO. This was exercised with
silence on Linux/WSLg on 2026-10-25. It is **not yet an implementation of
`audio.Device`**, physical synchronization evidence, or a production replacement
for the existing backend.

Unmodified Oto's public API is insufficient for the current output contract.
The experiment makes the missing backend operations explicit rather than using
`time.Now()` plus a requested buffer size as a fabricated hardware timestamp.

## Pinned source and scope

- Oto **v3.5.1**, commit `2d91d09885662153d86d0c6c8d4b716c232c1c9a`.
- Module zip SHA-256:
  `1d019a40a5a4107e2b4e8d5ef16b0cfee09d22576c4797508913e6d30ba674ea`.
- PulseAudio protocol dependency: `github.com/jfreymuth/pulse v0.1.3`.
- Independent experiment module, source copy, patches, executable binaries and
  observations live under `var/oto-experiment`. These are disposable local
  experiment files, not a shipped fork or a checked-in SDK dependency.
- Original Oto license notices remain in the local source copy. No upstream
  issue, pull request, registration or publication was made.

The prototype extends Oto's actual `pulseContext`, not an unrelated second audio
connection. `GetPlaybackLatency` queries use that context's own stream index.
The reply exposes server read/write byte cursors, running state and sink latency;
request RTT is retained separately. Its local midpoint is only a correlation
estimate, not an exact physical DAC timestamp. A server read cursor is not itself
the physical playback cursor.

## Experiments performed

The probe creates a single 48-kHz stereo float32 context with a requested 40-ms
device buffer and a 2,048-byte player buffer. Its reader returns **only zero
samples**. Each phase records 25 timing responses:

1. Play silence.
2. Reset the player, clearing its software buffer.
3. Resume playback.
4. Pause and stop source reads.
5. Use the experimental stream-level stop/flush operation.

In one recorded run:

| Observation | Result |
| --- | --- |
| Public player buffer after reset | 0 bytes |
| Server stream queue after reset | about 10–30 ms |
| Pre-reset write frontier still ahead of the first post-reset server read cursor | 3,832 bytes |
| Reported sink latency during reset | about 31–56 ms |
| Timing-query median RTT during reset | about 323 microseconds |
| Extended flush | empty server stream, corked, cursor stopped across all 25 subsequent samples |

Values vary with the actual server and system load. Earlier startup measurements
also contained a query RTT of about 15 ms; small median RTT is not a universal
accuracy bound. WSLg's server-reported latency is not a measurement of Windows
endpoint or speaker output accuracy. No loopback/impulse measurement was made.

### Flush transmission barrier

Player reset alone does not establish that earlier samples have left Oto's
callback/send pipeline. The prototype:

1. Counts every byte returned by the native-stream reader, including silence,
   relative to the initially empty stream's byte cursor.
2. Suspends future callbacks under the same lock that protects this counter.
3. Waits until the server's acknowledged write cursor includes every counted
   callback byte. Merely waiting for the callback's reader to finish is not enough:
   the PulseAudio library sends its returned buffer afterwards.
4. Corks and flushes that stream, checking the subsequent timing response.

The live `--send-window` fault test deliberately pauses a callback between
rendering/counting its bytes and handing them to the PulseAudio sender. It waits
for the flush to observe the incomplete server write frontier, then releases the
sender. Flush completes only after the pending write is included and discarded.
This is a channel-coordinated interleaving, not a sleep used to guess whether a
race occurred. Both the normal and race-instrumented binaries passed this test.
Five repeated runs of each binary also passed (10/10 controlled interleavings).

This proves server-stream queue handling in the tested scenario, **not immediate
physical silence**. Data already mixed into a shared sink or queued further
downstream cannot be retracted by flushing this stream. The experiment also does
not implement a complete resumable/closable device lifecycle.

## Build and validation boundaries

- Linux/amd64: built with `CGO_ENABLED=0`; real silence-only timing/flush probes.
- Linux/amd64: separate race-instrumented build; timing/flush and delayed-send
  probes completed without a race report.
- The locally extended Oto module's upstream Go tests passed with
  `CGO_ENABLED=0`, including its mixer tests.
- Windows/amd64 and macOS/arm64: cross-built with `CGO_ENABLED=0`.
  Their experimental timing/flush methods deliberately return
  `ErrSendspinTimingUnavailable`; **no native timing implementation or runtime
  validation on those platforms is claimed**.
- At the experiment stage, no Oto requirement or replacement was added to the
  SDK/example. The later integration noted above adds stock Oto, not this local
  source extension. Ordinary SDK lint and race tests pass with that integration.

Local experiment artifacts:

| Path under var/oto-experiment | Contents |
| --- | --- |
| `source.json` | Source pin and archive digest |
| `oto-extension.patch` | Exact changes relative to the pinned Oto archive |
| `main.go`, `summarize.py` | Silence-only probe and trace summary |
| `observations.jsonl`, `summary.json` | Raw protocol observations and summarized evidence |
| `observations-race.jsonl` | Race-instrumented live probe |
| `send-window.json`, `send-window-race.json` | Delayed-send barrier verdicts |
| `send-window-repeated.json` | Five additional runs per binary, all successful |
| `bin` | CGO-free target builds and Linux race-instrumented probe |

## Remaining work before integration

1. Define a supported timing API in Oto: atomic correlation between backend
   frame cursor, timestamp, latency, uncertainty and a timeline generation.
   An arbitrary player's front-end buffer and asynchronous mixer must map to
   the same backend timeline; separate unsynchronized counters are insufficient.
2. Implement corresponding feedback for WASAPI and Core Audio. Investigate ALSA
   separately; the prototype refuses its fallback rather than inventing a clock.
3. Specify stop/flush/drain/resume/close semantics, including downstream audio
   already mixed into shared output and bounded cancellation of backend queries.
   The probe's single stop/flush is not that complete lifecycle.
4. Resolve Oto's one-context-per-process and immutable output-format constraints
   for device reopening, multiple SDK clients and format changes.
5. Measure actual output timing and drift on each target platform. Protocol
   feedback alone does not prove RC1's physical +/-1-ms requirement.

The next integration decision is whether to maintain/propose these Oto API
extensions. A production backend must not depend on reflection into unexported
fields, `go:linkname`, arbitrary sleeps, or undocumented object layouts.

References: [Oto context API][context], [player API][player],
[PulseAudio driver][pulse], [PulseAudio protocol timing fields][protocol].

[context]: https://github.com/ebitengine/oto/blob/2d91d09885662153d86d0c6c8d4b716c232c1c9a/context.go
[player]: https://github.com/ebitengine/oto/blob/2d91d09885662153d86d0c6c8d4b716c232c1c9a/player.go
[pulse]: https://github.com/ebitengine/oto/blob/2d91d09885662153d86d0c6c8d4b716c232c1c9a/driver_pulseaudio_unix.go
[protocol]: https://github.com/jfreymuth/pulse/blob/v0.1.3/proto/op.go#L287-L302
