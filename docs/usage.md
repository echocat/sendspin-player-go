# SDK usage

## Lifecycle and ownership

`Config.Specification` explicitly pins the protocol profile. Empty selects
`1.0.0-rc1`; no other profile is implemented yet. An unsupported profile fails
before reading or creating credentials. This is not wire-level version negotiation.

One `Client` and one persistent `Store` represent one device. Do not share a
credential path between concurrently running instances. `New` validates config,
loads credentials, and persists a new random identity before opening a connection.
Storage failures are returned rather than silently replacing an identity.

- `Connect(ctx, url)`: one outbound session; cancellation disconnects it.
- `Run(ctx, url)`: outbound session plus capped exponential reconnect backoff.
- `DiscoverAndRun(ctx)`: discover, choose the first endpoint, then reconnect there.
- `Serve(ctx, listener)`: inbound listener and mDNS advertisement; cancellation
  owns listener shutdown and interrupts upgraded connections.
- `ServeHTTP`: an existing HTTP server can mount the client. Its owner must cancel
  request contexts for upgraded WebSockets at shutdown; HTTP Shutdown alone is insufficient.

RC1 uses plain ws:// with mandatory Noise encryption. Connect rejects wss URLs;
ServeHTTP rejects a direct TLS request before upgrade. Use a plain listener for
Serve. A reverse-proxy owner is responsible for the externally exposed transport;
untrusted forwarding headers are not used to infer or bypass transport policy.

Core clock messages and `LeaveGroup` do not require the player or controller role.
With an explicitly empty `Roles` slice the client still performs the core
handshake/activation/time exchange; it does not advertise source or custom roles.

Automatic announcements use the stable ClientID as service instance and publish
only addresses reachable through the listener. Use `DisableDiscovery` for loopback
listeners or when your application owns registration. A failed accepted Serve call
also closes its listener; an immediately rejected mode conflict does not take
ownership. IPv6 wildcard wrappers must expose `SyscallConn` for family detection.
See [discovery details](discovery-testing.md), including scoped IPv6 limitations.

Inbound and outbound modes cannot run simultaneously. Incoming provisional
connections are capped below pairing-record capacity. Arbitration follows activity
priority and the persisted last-playback server. This implementation does not admit
an additional concurrent pairing session alongside playback (the spec permits refusal).
Only an attempt that has actually started is protected against displacement;
that protection ends when pairing completes or aborts. Pairing records selected
by provisional handshakes are protected against eviction before activation, too.
Protection starts before the first pair-init write can reach its peer. Attempt
start and a competing handoff decision share a lock: whichever is decided first
wins; an already-retiring connection cannot subsequently start a pairing attempt.
Each attempt has a two-minute local deadline. Expiry sends `pair/abort` with
`attempt_timeout`, releases attempt protection and discards pending credentials.
The deadline is checked after sends and at acceptance of a finalize, not just when
the actor happens to select a timer event. A peer may already have stored its key;
local timeout cannot roll back remote storage. A new activation permits a retry.
Blocking application storage/callbacks are not a hard-real-time scheduling guarantee.
Pairing persistence receives a child context with the attempt deadline, so a
context-cooperative `Store.Save` cannot hold that attempt open indefinitely. If
that deadline expires before the Store commits and it returns the deadline error,
the SDK aborts the attempt. An earlier Store/parent timeout remains its own error,
not a fabricated `attempt_timeout`. The SDK cannot undo an already completed
remote save or safely preempt native I/O/a custom Store that ignores context.
The first-activation budget includes the handshake and is capped at 30 seconds,
even when a longer `HandshakeTimeout` is configured. A shorter local timeout is
allowed. Displacement waits for the post-re-handshake activation before sending
application-level goodbye under the new keys.
The time filter is retained across re-handshakes. Outstanding clock requests are
restarted because a server may discard old-key application messages; late replies
never update the filter or consume a newer request.

Initial handshake text is bounded to 128 KiB so a full 65535-byte Noise message
fits its larger base64/JSON carrier. After key installation, each binary transport
message remains capped at 65535 bytes (including its AEAD tag); larger application
messages use the RC1 fragmentation layer. A re-handshake does not raise that cap.

Do not call blocking SDK operations from `Config.Decoded`; that diagnostic hook
runs on the network actor. It is not an audio device callback. Copy any samples
that must outlive the invocation. Other event consumers can call client methods.

## Roles and state

`Snapshot().ServerLanguages` is the admitted server's optional ordered language
hint, copied for the caller. Nil means the server omitted it; no default language
is invented. Original case, order and repeated tags are preserved, and consumer
changes cannot alter SDK state. These connection-wide hints survive role changes
and Noise re-handshakes; a new connection gets its own hello state. The server's
wire name is authoritative over discovery hints, including an empty friendly name.

The SDK validates non-empty lists, ASCII RFC 5646 tag structure and duplicate
variant/extension singletons. It does not enforce a dated IANA registry or
extension-specific semantics, canonicalize tags, or implement language lookup.
Applications choose how to use these informational preferences for their own UI.
See [the audit's exact language-tag policy](rc1-audit.md#informational-language-tag-policy).

Default roles are player, controller and metadata. Explicitly set `Roles` to enable
artwork, visualizer and color. Artwork requires 1–4 channel configurations; visualizer
defaults to loudness and peak at a maximum 30 periodic frames/second.

`Snapshot` returns a defensive copy. Metadata and colors take effect at their
server timestamps using the current filter estimate. Future updates replace the
one pending update for that role. Removed roles discard state and pending updates.
`Snapshot.ActiveStreams` lists sorted, unversioned role families (`artwork`,
`player`, `visualizer`); stream end does not itself deactivate a role.

`CurrentPosition()` returns the applied metadata's extrapolated position in
milliseconds. It returns false until the connection is synchronized or when no
position is known. It never extrapolates a pending track's metadata. The lower-level
`Metadata.Position` expects server-clock microseconds, not Unix time.
Positions are clamped to the reported duration when known; an unknown duration
uses saturating int64 milliseconds. The integer formula preserves paused positions
above 2^53 and small clock differences at large timestamp origins, rounds down to
whole milliseconds after clamping at zero, and cannot overflow. This does not
improve the clock filter's own uncertainty or establish physical display timing.
Explicit track numbers start at 1; omission (represented as Track == 0) means unknown.

`CurrentArtwork(channel)` returns a defensive copy of the currently applied image,
not a future/pending one. False means no image (including clear, stream end, role
removal or disconnection) or an invalid channel. Getter results and artwork event
bytes are application-owned: changing either cannot alter the SDK's image cache.
During unavailability, discarded transfers still undergo full sequence/size
validation without retaining their data. A partly discarded image cannot become
current if availability returns before its final part.

Artwork bytes are encoded image data: the SDK owns transfer accounting, scheduling
and defensive copies, while the application's image decoder/renderer owns content
validation and painting. Config.Artwork is fixed for that Client; live display-layout
reconfiguration is not currently exposed. A cancel drops only the targeted channel's
pending/partial image; stream end or role removal also clears displayed artwork.

Config.MaxMessageBytes also bounds each announced encoded image (default 8 MiB,
maximum 64 MiB). This limit is local and not advertised as an artwork wire budget.
Choose dimensions/PNG-versus-JPEG and the limit accordingly; large otherwise-valid
images can exceed it. Current/pending images, notifications and caller-owned copies
have additional memory costs. This is distinct from the 65519-byte per-message cap.

Events are best-effort notifications on a 64-entry buffer. They are not a lossless
media transport or a raw protocol trace. Applications needing every visualizer
frame should account for this limitation before enabling that role.
Use the read models to recover current artwork/state after missed notifications,
and consult `ActiveStreams`/`ActiveRoles` before rendering buffered application
events after a stream or role has ended. An application is still responsible for
consuming and presenting visualizer events promptly; they are not a hardware
real-time presentation API.

Visualizer requests are fixed in Config.Visualizer for a Client; an explicit empty
Types slice requests no data. RateMax caps each periodic type separately and does
not cap beat/peak events. A supplied Spectrum must be valid even if that type is
not requested. Server echoes may omit unavailable types, but their conditional
tracks_downbeats/spectrum fields must match the echoed type set and spectrum must
include all requested fields. In-place updates preserve timestamp ordering; only
clear/new-stream boundaries reset it.

Visualization.Values keeps raw wire values: loudness/spectrum amplitudes use the
uint16 dB-scaled range, f_peak contains frequency then amplitude, and peak uses
Strength. The SDK does not compute FFT/tempo/onset analysis. Reserved beat high
bits are ignored; they cannot leak into Downbeat or Strength. Already-stale frames
are dropped before event delivery; cleared, ended and removed streams discard
buffered future data. A renderer must still reject its own queued obsolete events.

The sender must send a current snapshot before scheduling state on a newly added
metadata/color role. A receiver cannot prove that sender-clock requirement from an
uncertain local estimate alone, so this SDK follows the usual pending-state rules
even when its first state maps to the future. Later clock samples can make it due
without a new server/state. Authoritative sender conformance checks need evidence
from the sender's own clock.

Local controls: `SetVolume`, `SetMuted`, `SetOutputDelay`, `SetAvailable`,
`PreferFormat`, `LeaveGroup`, and controller `Command`. Controller parameters use
pointers so zero/false are distinguishable from absent values. A preferred format
must have been advertised. Output delay is clamped to 0–5000ms and persisted.

`Command` targets the **server's group**, while `SetVolume`/`SetMuted` change this
player's own state. A controller command needs an active controller role and a
complete current `Snapshot().Controller` offering that command. Absolute seek also
needs the latest `SeekMaxMS` and a position in 0..maximum. Relative seek forwards
the signed offset unchanged; the server owns best-effort clamping. Missing or
foreign parameters are rejected locally, without sending a command or closing the
session. Optional state fields are replaced, not merged from a previous snapshot.

Remote player commands remain effective while the player role is active but
locally unavailable. Successful changes persist before a full client/state echo;
volume does not clear mute, and delay input is clamped before conversion to machine
int. A valid string naming an unoffered player command is ignored, including its
argument body. This is not an escape from active-role/JSON checks or from strict
argument validation for volume, mute and set_output_delay.

## Audio output contract

Implement `audio.Output.Open` and `audio.Device`:

- `Next(ctx)` returns the predicted audio-port time of the first frame of the next
  Write, including queued frames and DAC latency, in Go's monotonic time domain.
- `Write(ctx, samples)` accepts interleaved float32. It copies retained samples and
  applies bounded backpressure. It must honor cancellation promptly.
- `Flush(ctx)` immediately discards queued output and resets its timeline.
- `Close()` releases resources and stops remaining output.

These are the strict output guarantees. A separately documented best-effort mode
may relax device-clock and downstream-flush guarantees only with explicit local
opt-in. The Oto backend below is such a mode, not a conformant substitute for
hardware-clock feedback. This choice adds no wire capability or negotiation.

Only one worker calls a device. Network reception never waits for its write buffer.
Chunks retain their negotiated format even after an in-place `stream/start`.
Changing output rate/channels waits for the preceding format's queued tail; list
only formats the backend can switch between acceptably. Steady-state corrections
drop/duplicate small whole-frame steps, capped at 0.5% per chunk and by a shared
750µs absolute-edit budget in every 150ms of the predicted output timeline. The
budget persists across rate changes and stream resets; opposite corrections do
not cancel each other. Startup, underrun and errors beyond 1ms use discrete
resynchronization. Gain changes use a short ramp. Waiting chunks re-read changed
output delay/gain and the current clock estimate before submission.

Timestamp arithmetic does not wrap an extreme past deadline into the future (or
vice versa). Mappings outside the supported integer range, or a nonfinite/reversed
clock model, terminate scheduling with an error rather than playing at a fabricated
deadline. This is a numerical safety boundary, not a physical clock-accuracy claim.

Clear/end/role-removal invalidate both queued chunks and the current chunk's
context, then flush on the output worker. A reset during a format drain skips the
obsolete format; fresh data is not submitted after a failed flush. Drain/open/write
errors terminate the worker unless they report cancellation of that actual work
context. Merely returning `context.Canceled` while the context is live is a backend
failure, not an implicit reset. A re-handshake alone retains the open stream,
device and buffered audio; it is not a clear/stop operation.

The default formats all use 48kHz stereo. Configure `Formats` to request other
combinations. An Opus entry must be accompanied by PCM or FLAC; default decoder
factories support all three, but Opus is not requested by default.

`codec_header` is standard RFC 4648 Base64, not Base64URL or MIME input. Wire
validation rejects CR/LF, missing required padding, invalid alphabet/unused bits
before invoking any custom decoder factory. A custom factory still owns the
codec-specific header/frame contents it accepts. The public built-in FLAC decoder
independently enforces the same lexical encoding plus STREAMINFO contents.

`RequiredLeadTime` and `MinBuffer` are application-chosen hints. They are not
automatically calibrated in this implementation. Physical sync, correction
audibility, and startup artifacts still need hardware/loopback verification.

## Audio backend comparison

| Backend | Build requirements | Timing | Clear/close limits |
| --- | --- | --- | --- |
| `audio/portaudio` | Independent Go module; CGO plus PortAudio headers/library | Native DAC timestamps and stream clock | Native driver behavior; blocking C calls cannot be interrupted by Go context |
| `audio/oto` | Oto v3.5.1; Windows/macOS/Linux can build without CGO | Explicitly enabled software-queue/latency estimate, no device clock | Clears SDK/player software queues only; downstream audio tail may remain |
| `audio/alsa` (unreleased) | Independent Linux module; CGO plus libasound headers/library; no Oto/PortAudio | MONOTONIC PCM status and reported delay; non-HW plugins require explicit opt-in | Native drop and conservative reported-tail wait; driver/FIFO accuracy unmeasured |
| `audio/native` (unreleased) | Independent CGO module; PipeWire on Linux, system WASAPI on Windows, Apple frameworks on macOS | Native graph/endpoint/host clocks and bounded callback queue | Native stop/reset and reported-tail wait; physical downstream behavior unmeasured |

No backend has a physical +/-1-ms certification. PortAudio is the released
cross-platform choice when hardware feedback is required; Oto is the convenient
best-effort choice when its limits are acceptable. The new direct Linux ALSA
implementation and its separate validation scope are described in
[Linux ALSA output](linux-alsa.md); it is not included in v0.1.1.
Direct desktop APIs and their platform-specific build/evidence boundaries are
documented in [native desktop outputs](native-desktop-audio.md).

## Native PortAudio backend

Import `github.com/echocat/sendspin-player-go/audio/portaudio` and use
`portaudio.Output{}`. Its source and tests use Go's automatic `cgo` constraint.
Importing it with CGO disabled fails at build time; no custom tag is necessary.
The independent module boundary keeps ordinary root `go test -race ./...` free
of native development-header requirements. Importing the SDK core does not load it.

- Linux: a C compiler, `pkg-config` and PortAudio development package (Fedora:
  `portaudio-devel`; Debian/Ubuntu: `portaudio19-dev`).
- macOS: Xcode command-line tools and PortAudio, for example through Homebrew.
- Windows: a compatible C toolchain, PortAudio library/headers, and a working
  `portaudio-2.0.pc`/`PKG_CONFIG_PATH` configuration. A matching runtime DLL must be
  deployable. This setup has not been exercised locally.

The backend uses callback DAC timestamps and PortAudio's stream clock, not sleeps
as a substitute for a hardware clock. Its application ring buffer is bounded. A
driver that supplies no usable DAC timing is rejected. Driver calls such as open,
abort and close can still block in native code; a context cannot interrupt those
C calls. Validate the driver and configured latency on each supported platform.

## CGO-free Oto backend

```go
import "github.com/echocat/sendspin-player-go/audio/oto"

// Explicitly accept estimated timing and downstream audio tails.
output := oto.Output{AllowApproximate: true}
```

The zero value refuses to open with `ErrApproximateOnly`, before creating a native
context. This avoids silently presenting an estimate as DAC-clock feedback.
`Next` adds the bounded SDK ring, Oto's player buffer and
`EstimatedDeviceLatency` to the current monotonic time. The latency is a calibration
hint (default: the requested device buffer, 40 ms), not measured feedback; hardware
drift, underruns, audio-server buffering and scheduling can invalidate it. Reducing
the configured buffer does not establish a tighter timing-error bound.

`Flush` pauses and stops source reads, seeks the live source to clear both software
queues, then resumes. `Close` stops those reads and clears the software queues.
Neither can retract already mixed/submitted audio from the OS/server/device.
Consequently clears, stops, disconnects and server handoffs may have an audible
tail or overlap; strict output-stop guarantees do not apply in this opt-in mode.

Additional limitations:

- One retained Oto context per process, one active SDK output device at a time.
  `ErrInUse` rejects concurrent devices. Another library creating its own Oto
  context can prevent this backend from initializing.
- Sample rate, mono/stereo channel count and requested device-buffer duration
  remain fixed after first initialization. Reopen with the same configuration is
  supported; a change returns `ErrContextFormat`. Advertise one rate/channel layout
  across all codecs. The example uses 48-kHz stereo throughout.
- A failed Oto context cannot be replaced in-process. Waiting for asynchronous
  initialization can be cancelled, but a native/library initialization call itself
  is not interruptible. Close releases the SDK lease, not Oto's process resources.
- Windows, macOS and Linux builds need no C compiler for this backend. Runtime
  audio services/frameworks are still required. Linux uses PulseAudio (including
  compatible PipeWire servers), with a dynamically loaded `libasound.so.2` fallback.
- Unit tests use fake device/player interfaces and open no hardware. A local
  Linux/WSLg smoke test uses silence only; Windows/macOS runtime and physical output
  measurements remain outstanding.

The shipped backend uses **unmodified upstream Oto**. The experimental same-stream
PulseAudio timing/flush extension described in [the feasibility report](oto-feasibility.md)
is not shipped as a fork or enabled implicitly. Accurate timing, full lifecycle
semantics and corresponding Windows/macOS APIs would be separate future work.

## Credentials

`FileStore` saves a complete snapshot by same-directory temporary file, fsync and
atomic replacement. Unix files are private (0600); reads reject public permissions.
Directory durability across sudden power loss and Windows ACL provisioning are not
implemented by the SDK. Supply a custom Store for stronger platform guarantees.

Default record capacity is 16 (minimum 5). Eviction selects the oldest record not
backing an open connection. PSK hashes are always bound to their stored server ID.
Only the initial handshake allows Sentinel fallback. A fallback never deletes or
overwrites a record. Formatting secrets is redacted; explicit serialization is
confidential and remains the application's responsibility.

## Optional diagnostics

`Config.Observe` accepts a `diagnostic.Observer`. It sees successful sends and
authenticated receives at the complete-WebSocket-message boundary (plus explicitly
marked logical reassembly observations). JSON is projected to a fixed field
allowlist; unknown fields and Noise/pairing bodies are not exposed. Binary data
contains at most the 13-byte audio header, or just the type byte for other messages.
`Truncated` means the projection is incomplete. Observations are application-owned;
callbacks may retain/mutate their payload/prefix without changing SDK buffers.

This is not a raw packet logger or a production monitoring guarantee. Display names,
public identities and timing can still be personal data. Callbacks run inline,
may be concurrent across send/receive paths and connections, must return promptly,
must not panic, and must not call client methods. Their order across concurrent
paths is not a serialized wire trace; `At` captures the observation boundary before
the callback, not a physical network timestamp. A slow callback affects processing
despite receive timestamps being captured before observation. Nil disables projection.

`Config.Encoded` is separate: it exposes actual private audio payloads before
decoding, using borrowed read-only slices valid only during the callback. Copy to
retain. Like `Decoded`, it runs on the protocol actor and must not call client
methods. Neither callback measures hardware output. The conformance adapter uses
these hooks to record evidence without logging secrets or audio content.
