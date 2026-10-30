# SDK usage

**Development / unreleased:** `v0.1.1` remains the recommended stable release,
but does not contain the new Source, PIN, host-managed player-control or purego
backend APIs described here. The native backend migration is still WIP. Use a
matching development checkout when evaluating these APIs; historical release
tags are unchanged, and no new version or successful CI run is implied.

The SDK, backends and `audio/native` selector share the root module; import paths
are unchanged. Each real example has its own `go.mod`/`go.sum`, requiring SDK
`v0.1.1` with a local `replace` to `../..` for these unreleased APIs, not a separate
native-module requirement. From the checkout root, use
`go -C _examples/player run . -h` or
`go -C _examples/external-control run .`. See [module layout](module-layout.md).

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
artwork, visualizer, color or Source. Artwork requires 1–4 channel configurations;
visualizer defaults to loudness and peak at a maximum 30 periodic frames/second.

`Snapshot` returns a defensive copy. Metadata and colors take effect at their
server timestamps using the current filter estimate. Future updates replace the
one pending update for that role. Removed roles discard state and pending updates.
`Snapshot.ActiveStreams` lists sorted, unversioned role families (`artwork`,
`player`, `visualizer`, and opt-in `source`); stream end does not itself deactivate a role.

For an explicitly configured `SourceRole`, `Input` and `SourceFormat` select
capture sent **to** the server, not external playback takeover. The stream family
`source` appears in `ActiveStreams` only after `client-stream/start`. Neither
activation nor availability initializes capture; only a valid server source
`start` can do so. See [source API, privacy and backend contract](source-input.md).

Source and native Input are separate explicit choices: configure an `audio.Input`
and include `SourceRole`. Imports, `New`, handshake, availability, reconnection
and role activation alone never call `Input.Open`. Capture requires an active,
available, synchronized source and a new authorized server start; stop,
unavailability, role loss and disconnect clear authorization. Unpaired source
access requires explicit server approval, not approval implied by player use;
`AllowUnpaired` remains false by default. Captured samples never feed local Output.
Player `available:false` instead reports external playback takeover, not an input.

Optional static/dynamic PIN pairing is also unreleased and explicit through
`Config.CodePairing`; nil preserves the default PSK-only offer. See
[PIN pairing](pin-pairing.md) for method selection and host UI/security duties.

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
locally unavailable. In the default software-control mode, successful changes
persist before a full client/state echo; volume does not clear mute, and delay
input is clamped before conversion to machine
int. A valid string naming an unoffered player command is ignored, including its
argument body. This is not an escape from active-role/JSON checks or from strict
argument validation for volume, mute and set_output_delay.

### Host-managed or fixed player volume/mute (unreleased)

`Config.PlayerControl == nil` retains software gain/mute, its short ramp and
persisted software values. A non-nil `*PlayerControlConfig`, **even an empty one**,
selects immutable unity-gain output without software mute/ramping. Without a bound
target this is a fixed/no-controls player, never a fallback to software control.
It does not set a hardware volume to 100 or replay stored software settings.

Use `BindPlayerControl` for explicit host setters and initial observations, and
`ReportPlayerControl` for separate actual readback. Construction, binding, reports,
activation and reconnect do not invoke hardware setters. Local SetVolume/SetMuted
success confirms bounded queue acceptance, not hardware state; a successful setter
also does not establish readback. Retain the binding returned by each report.
`Snapshot.VolumeKnown`/`MutedKnown` distinguish unknown state from 0/false;
capabilities and known values control which absolute setters are advertised.
External observations/requests are not persisted; output-delay persistence remains.

The host owns CEC, mixer adapters and HDMI/appliance policy. No such integration
is built in or demonstrated by fake-control tests. See
[the exact external player-control contract](external-player-control.md) for
generation changes, overload, cancellation, request status and callback ownership.

## Audio output contract

Implement `audio.Output.Open` and `audio.Device`:

- `Next(ctx)` returns the predicted audio-port time of the first frame of the next
  Write, including queued frames and DAC latency, in Go's monotonic time domain.
- `Write(ctx, samples)` accepts interleaved float32. It copies retained samples and
  applies bounded backpressure. It must honor cancellation promptly.
- `Flush(ctx)` immediately discards queued output and resets its timeline.
- `Close()` releases resources and stops remaining output.

These are the strict output guarantees, not a claim that a replacement backend
or driver has already demonstrated them physically. Any explicitly documented
weaker mode must identify its limits; native API timestamps alone do not prove
DAC accuracy or downstream silence. No wire capability is invented for this.

Only one worker calls a device. Network reception never waits for its write buffer.
Chunks retain their negotiated format even after an in-place `stream/start`.
Changing output rate/channels waits for the preceding format's queued tail; list
only formats the backend can switch between acceptably. Steady-state corrections
drop/duplicate small whole-frame steps, capped at 0.5% per chunk and by a shared
750µs absolute-edit budget in every 150ms of the predicted output timeline. The
budget persists across rate changes and stream resets; opposite corrections do
not cancel each other. Startup, underrun and errors beyond 1ms use discrete
resynchronization. In default software-control mode gain changes use a short ramp;
non-nil PlayerControl keeps unity gain without software mute/ramping. Waiting
chunks re-read changed output delay/gain and the current clock estimate before submission.

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

## Audio backend comparison (unreleased migration)

Concrete backends belong to the **root module** and use purego/system dynamic
libraries with `CGO_ENABLED=0`; no audio development headers or C compiler are
required for production builds. OS/architecture implementation files are selected
at compile time, not by a runtime GOOS switch. Native implementations are WIP,
targeting Linux/macOS/Windows amd64 and arm64; a cross-build is not native execution.

| Package | Platform / runtime dependency | Timing and validation boundary |
| --- | --- | --- |
| `audio/alsa` | Linux; dynamically loaded libasound | PCM MONOTONIC status, reported delay, native drop; non-HW plugin timing requires explicit AllowPluginTiming. Physical ADC/DAC/FIFO accuracy remains unmeasured |
| `audio/pipewire` | Linux; libpipewire and a usable graph | Graph/resampler/queue timing and bounded capture/render queues; a private software graph is not hardware evidence |
| `audio/wasapi` | Windows; system WASAPI/COM APIs | Endpoint audio clock/QPC correlation and explicit capture/render direction; native execution and physical output/input still need validation |
| `audio/coreaudio` | macOS; system audio frameworks | AUHAL host timestamps; device rate must already match. Native execution and physical output/input still need validation |
| `audio/noop` | No audio service or device | Explicit paced discard Output only; no capture, hardware clock, physical silence or synchronization evidence |
| `audio/native` | Root-module selector using the native backends | Selects a backend through compile-time platform files; adds no independent timing guarantee |

No backend has physical +/-1-ms certification. Oto and PortAudio are removed from
the current backend surface; their historical release implementations/results do
not validate these replacements. See [native tests and explicit device opt-ins](native-audio-tests.md),
[direct ALSA](linux-alsa.md) and [desktop API details](native-desktop-audio.md).

### Selection and library failures

The optional selector exposes `native.Output` and `native.Input` with `Backend`,
`Device` and `SharedLibPath`. Linux auto selection prefers usable PipeWire, then
tries ALSA when PipeWire is unusable. Windows selects WASAPI; macOS selects
CoreAudio. Explicit backend selection does **not** fall back to another backend.
An explicit library path must load exactly that library: a missing file or symbol
is an error, not permission to try the default library or another backend. Linux
auto with an explicit library path likewise does not fall back to ALSA.

`noop.Output{}` or `native.BackendNoop` is an explicit no-device choice, never an
automatic rescue for native failure. Noop has no Input. Constructing/configuring
a native backend is inert; runtime libraries/services and device permissions are
needed only when it is opened. A Source Input is opened solely by its authorized
server-start lifecycle, not by selecting an output or enabling a monitor implicitly.

### Input contract and safety

`audio.Input.Open(ctx, format)` returns an `audio.InputDevice`; its
`Read(ctx, dst)` returns a **sample count** (a channel multiple) and the best-effort
ADC time of the first sample in the local Go monotonic time domain. `dst` is
borrowed only until return. Backends bound capture queues and report discontinuity
through `audio.InputDiscontinuityError`; cancellation must return promptly before
the owning worker closes the device. Read arrival is not a substitute for ADC time.

These APIs do not change global audio defaults, input gains, mixers, monitoring
routes or hardware volume. PipeWire/WASAPI monitor capture is a separate explicit
loopback choice, not an automatically selected microphone. Source/native-input
fake tests establish neither physical capture timing nor acoustic/electrical safety.
See [Source API](source-input.md) and [native test opt-ins](native-audio-tests.md)
before opening any input or running a marker/physical-loopback test.

The new amd64/arm64 Linux/macOS/Windows CI matrix is configured but **has not run**
for this unpushed migration. Local tests/cross-builds, skipped device tests and
historical artifacts must not be reported as its successful execution. No hardware
tests have been performed for this migration.

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

Runtime SDK/backends use only the base slf4g facade and optionally injected
loggers. The host selects any provider; imports, capture and host-control binding
do not install one. Samples, credentials and raw backend errors are not normal log fields.

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
