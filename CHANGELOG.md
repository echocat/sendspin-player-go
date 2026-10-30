# Changelog

## v0.2.0

### Added

- CGO-free native playback and capture through purego: ALSA, PipeWire, WASAPI and
  CoreAudio. `audio/native` selects the platform backend; Linux auto-selection
  prefers usable PipeWire, then ALSA. Explicit backend/library choices fail
  without hidden fallback. `audio/noop` is an explicit output-only discard option.
- Opt-in `source@v1` with server-authorized PCM16 upstream, bounded capture queues,
  drift-aware timestamps and ordered stream lifecycle. No implicit capture,
  automatic capture resumption or direct local monitoring.
- Backend-neutral host-managed volume/mute with capability/readback/request
  separation, bounded serialized writes, cancellation and stale-target protection.
  Nil `PlayerControl` retains legacy software gain; non-nil external/fixed mode
  uses immutable unity gain without software fallback or implicit hardware writes.
- Optional dynamic six-digit and static eight-digit PIN pairing with CPace,
  attempt/counter limits, persistence hardening and independent interoperability
  checks. PSK/token pairing remains the default; select at most one PIN method.
- Independent Source/control/PIN tests, native ABI and opt-in device tests,
  private PipeWire scenarios, offline WAV analysis and hardware-free task tests.

### Changed / migration

- **Removed `audio/oto` and `audio/portaudio`.** Use `audio/native` or a concrete
  native backend, or supply your own `audio.Output`. Native builds need runtime
  system libraries/services and permissions, not audio headers or a C compiler.
- SDK, backends, selector and internal Go tools now share one root Go module.
  Remove separate SDK-backend module requirements/replacements. Each real example
  keeps its own module and application-owned dependencies/logging provider.
- Removed the audio-probe example; offline loopback tooling is internal.
  MISE now uses executable file tasks with shared helpers outside task discovery.
- Added daily grouped Dependabot updates for Actions, the SDK and example modules.
- Hardened backend lifecycle, callback/thread ownership, timeline recovery and
  deterministic cancellation/shutdown test fixtures.
- Added `filippo.io/edwards25519 v1.2.0` for CPace; purego and x/crypto are now
  direct dependencies. Removed Oto and its PulseAudio dependency; remaining
  runtime dependency versions are unchanged from v0.1.1.

### Requirements and limits

Requires Go **1.27.3+**. Production SDK/backend builds support `CGO_ENABLED=0`;
the Go race detector still needs its own C toolchain. The Sendspin specification
pin remains `671a34d408581fc25ea56b3528a4a3f13e3be901` (`1.0.0-rc1`); core wire
and role versions do not change. Independent interoperability uses unchanged
`aiosendspin[server]==10.2.1`; tooling/dependency pins remain in `go.mod`,
`mise.lock` and the locked Python fixtures, without a peer upgrade.

Software/ABI/isolated-graph checks are not physical synchronization or complete
RC1 certification. Native input, Windows/macOS hardware, calibrated timing and
analog stop/flush/click behavior require separate acceptance. The strict
conformance gate remains blocked; independent cryptographic review and optional
PIN product acceptance remain open. Historical tags/releases are unchanged.

## v0.1.1

- Refresh mDNS advertisements after interface/address changes, withdrawing stale
  bindings while preserving unchanged registrations and listener restrictions.
- Fix cross-interface mDNS query/record processing with a private, source-pinned
  adaptation and verified kernel receiving-interface filtering.
- Support Windows packet-info/scoped UDP replies without CGO.
- Add nonblocking advertisement error notifications and recovery health snapshots.
- Validate IPv4/IPv6 publication and cleanup with an independent isolated observer.

Requires Go 1.27.3+. This release does not claim complete RC1 conformance or
physical audio synchronization. The published v0.1.0 tags remain unchanged.

## v0.1.0

Initial release
