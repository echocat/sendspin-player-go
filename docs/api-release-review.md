# Public API and release-package review

This review covers the reusable SDK, not a product certification or permission to
publish a version tag. Specification RC1, wire version 1 and SDK version remain
independent. No tag or binary release is created by the checks below.

**Release boundary:** `v0.1.1` is still the recommended stable release, without the
new Source/PIN/host-managed player-control and purego backend APIs. Those changes
are unreleased. Native output/input replacement implementations remain WIP; the
new Linux/macOS/Windows amd64/arm64 CI matrix is configured but has not run for the
unpushed migration. Local software tests/cross-builds are not native-platform,
hardware or hosted-CI acceptance. Historical releases/results remain unchanged.

## Findings addressed

- `ErrProtocol` is now a public alias of the existing protocol sentinel. A caller
  can use `errors.Is` without importing an internal package. It covers recognized
  shape/direction/sequencing errors; not every transport/authentication or custom
  backend/Store error is collapsed into that category.
- Credentials' record slices are detached at both custom Store boundaries. New
  and Save no longer expose the Client's live records through a Store-owned or
  Store-retained slice. Regressions deliberately use an aliasing Store.
- Package/API documentation distinguishes public ClientID from private identity
  material, ephemeral construction from started workers, listener ownership,
  callback restrictions, lossy events, readiness and external output delay.
- A runnable external-package `ExampleNew` constructs a controller without audio
  or networking and demonstrates the public disconnected error. It uses the
  recording logger; executable hosts may choose their own logging provider. The
  runtime SDK/backends depend on the base slf4g facade, not a provider.
- Explicit Source/Input and host-managed/fixed PlayerControl APIs are separate
  opt-ins. Configuration is not permission to initialize capture or write hardware.
  Their fake-input/control evidence does not promote historical normative assessments.

## Ownership, cancellation and errors

| Surface | Contract / review result |
| --- | --- |
| `New` | Copies mutable configuration, loads or creates/persists identity; starts no network, discovery, output/capture or host-control write worker. Constructor errors remain errors, not logged credential objects. |
| `Connect` / `Run` | One outbound reservation. Connect is one session; Run retries only the selected recoverable purposes/reasons with backoff. Cancel and join the returned operation; pairing-only loss is not silently resumed. |
| `Serve` / `ServeHTTP` | Serve owns/closes its listener and joins its handlers. ServeHTTP only owns its upgraded connection, not the host HTTP server. Inbound and outbound modes remain exclusive. |
| `DiscoverAndRun` | Chooses the first syntactically usable discovery endpoint; it is not an arbitrary-endpoint reachability probe or general multi-interface failover policy. Browse can be used for application selection. |
| `Events` | One bounded best-effort channel, not broadcast/durable delivery; stays open for Client reuse. Consumers need their own cancellation select. Snapshot/current-artwork reads retain latest state when events drop. |
| commands/settings | Serialized through the session actor, requiring an admitted session/active role. Argument pointers are copied. Validation, disconnected and canceled errors do not promise one universal precedence. |
| `Store` | Atomic full snapshots, one owning Client, cooperative deadlines. Save must not call back into Client methods because the transaction holds the credential lock. Cancellation cannot undo a committed remote/local change. |
| `Output` / `Device` | One render worker owns the device; borrowed samples must be copied if retained. Context cooperation is required. Hardware truth and downstream flush guarantees are backend contracts, not established by a virtual output. |
| `Input` / `InputDevice` | Explicit Input plus SourceRole; the Client calls Open only after an active/available/synchronized source receives a valid server start. Read returns a channel-multiple sample count and first-sample ADC time in local Go monotonic time; dst is borrowed, capture queues bounded, discontinuities explicit. Stop/removal/unavailability/disconnect clear authorization; no local input monitoring or automatic resume. |
| `PlayerControl` / host bindings | Nil keeps legacy software gain/persistence. Non-nil, even empty/fixed, means immutable unity gain without software mute/ramping. Bind/Report/start/reconnect never invoke setters or replay stored settings. Explicit writes use a bounded worker; request completion is not readback. Generation/capability/session changes invalidate old writes. No hardware-control adapter or software fallback is supplied. |
| Native backend selection | All purego backends and audio/native share the root module; the selector uses compile-time platform files. Linux auto prefers usable PipeWire, then ALSA; explicit backend/library-path failures return errors without fallback. Noop is an explicit output-only discard choice, never a failed-native or capture fallback. |
| observer/codec hooks | Prompt, nonreentrant, potentially concurrent where documented; payload ownership is explicit. Diagnostic hooks are not default logging. |
| logging | Only slf4g facade in runtime SDK; provider selected by the host, optional per-Client injection. Raw peer/backend errors, credentials and media are not default log fields. |

The SDK does not forcibly kill arbitrary user callbacks or native I/O. It offers
no live unpaired-policy setter, Factory Reset implementation, code-pairing UI,
automatic hint calibration or new role/core version under the guise of API polish.
Optional PIN methods require explicit configuration and host UI; Source unpaired
access needs separate explicit server approval, not ordinary player approval.
The SDK supplies no CEC, ALSA mixer or HDMI policy. See [Source](source-input.md)
and [external player-control](external-player-control.md) for the precise contracts.

## Release checks and retained limits

- Run `mise run check:full` on a clean matching checkout, validate all independent
  example modules and the optional selector separately, and run the configured
  native-backend ABI/software profiles and platform matrix. These are required
  checks, not a claim that the new workflow has already executed.
- The module archive test extracts and tests the distributable root with CGO
  disabled. Backends, selector, real examples and internal Go tools belong to
  that root archive. Runtime artifacts and Python harness environments remain
  outside it; shared caches/temp directories are untouched.
- Check separate consumer modules without local SDK replacements before a tag.
  A checkout-local replacement against the root is not proof that a published
  v0.1.1 dependency provides these new APIs. There is only one publishable Go SDK
  module/version; do not modify historical tags or invent an already released version.
- Oto/PortAudio source/jobs/tasks are retired in development. Historical callback
  or WSLg/silence-only results describe their original source, not replacement
  backends. Production purego builds require no audio development headers or C
  compiler; optional Go race instrumentation still needs its own C toolchain.
- Keep library-load/ABI, private null-PCM/graph, independent fake-device protocol,
  explicit device-loopback and physical timing evidence distinct. Skipped hardware
  tests are skips, never passes. Native implementation work is ongoing and no
  hardware tests have been performed for this migration. Follow the explicit
  device/privacy opt-ins in [native audio tests](native-audio-tests.md).
- Fixed independent Noise vectors, isolated IPv6/interface hotplug and sustained
  software soak add finite evidence, not physical synchronization or formal
  cryptographic assurance. The existing strict conformance gate stays blocked.

See [releasing](releasing.md), [discovery](discovery-testing.md),
[Noise vectors](noise-vectors.md) and [resource soak](resource-soak.md).
