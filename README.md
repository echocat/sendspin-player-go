# sendspin-player-go

A Go SDK for Sendspin players, targeting specification **1.0.0-rc1** at
[`671a34d`](https://github.com/Sendspin/spec/tree/671a34d408581fc25ea56b3528a4a3f13e3be901).
Requires **Go 1.27.3+**. The SDK core, including PCM, FLAC and Opus decoding, is
pure Go. Native audio output is optional.

**Status:** SDK **v0.1.1**; the API may change before v1. See [release notes](CHANGELOG.md).
This is not a declaration of complete RC1 conformance.
Independent server interoperability is tested; physical synchronization has not
been measured. See [coverage and remaining work](docs/conformance.md).
The [machine-readable requirement catalog and CI traceability report](docs/traceability.md)
separate reviewed requirements, actual test results and unresolved conformance gaps.

## Features

The SDK logs through the `github.com/echocat/slf4g` facade only. Applications choose
the provider/output (the player example chooses `slf4g/native`); `Config.Logger`
can inject a per-client logger. Default lifecycle logs omit raw errors, endpoints,
credentials and media. Sinks must return promptly; no audio-packet logging occurs.
Tests can use `slf4g/sdk/testlog` or `slf4g/testing/recording` without installing a
process-global provider.

- Client- and server-initiated WebSockets, with mutually exclusive discovery modes.
- [Dynamic mDNS publication](docs/dynamic-advertisement.md) preserves live listener restrictions.
- Noise `KKpsk2`, both RC1 cipher suites, PSK pairing, re-handshakes and unpairing.
- Persistent identity, pairing records, output delay, volume and mute.
- Player, controller, metadata, artwork, visualizer and color roles; individually enabled.
- PCM 16/24-bit, FLAC and [gopus](https://github.com/thesyncim/gopus) Opus decoding.
- Reference time-filter port, bounded playback queue, scheduling and drift correction.
- Scheduled metadata/colors, multipart artwork and visualizer buffering.
- Pluggable audio output and codec factories; CGO-free Oto and native PortAudio backends.

## Embed

```sh
go get github.com/echocat/sendspin-player-go@v0.1.1
```

```go
client, err := sendspin.New(ctx, sendspin.Config{
    Name:   "Living room",
    Store:  sendspin.FileStore{Path: credentialPath},
    Output: myAudioOutput, // implements audio.Output; real device-clock feedback required
    Roles:  []sendspin.Role{sendspin.Player, sendspin.Controller, sendspin.MetadataRole},
    MinBuffer: 100 * time.Millisecond,
    RequiredLeadTime: 250 * time.Millisecond,
})
if err != nil {
    return err
}
return client.Run(ctx, "ws://192.0.2.1:8927/sendspin")
```

Import `github.com/echocat/sendspin-player-go` as `sendspin`. `Run` reconnects
transient handshake, idle and playback failures; a dropped pairing-only session
requires operator-driven resumption. `Connect` runs one session.
`DiscoverAndRun` selects the first discovered server.
For server-initiated connections, pass a TCP listener to `Serve`; it advertises
`_sendspin._tcp` unless `DisableDiscovery` is explicitly enabled for controlled
environments. `ServeHTTP` integrates with an existing HTTP server; the application
then owns advertising and shutdown.

`RecommendedListenAddress` is `:8928`; `DefaultWebSocketPath` is `/sendspin`.
These are explicit defaults for integrations, not an automatically opened port.

Unpaired playback is **disabled by default**. Transfer `client.PairingToken()`
to the server's operator interface to pair. The token is confidential: never put
it in logs or a URL. `MemoryStore` is for tests, not persistent devices. On Windows,
applications must secure the credential directory's ACL.

Read `Snapshot()` for current state and consume `Events()` for notifications.
Events are bounded and best-effort; slow consumers do not block protocol processing.
`Snapshot().ActiveStreams` distinguishes an active role from an open stream.
Use `CurrentArtwork(channel)` to recover the current image even after missed events,
and `CurrentPosition()` for the synchronized playback position in milliseconds.
See [SDK usage and audio contracts](docs/usage.md).

## Player example

The example is an independent module under [`_examples/player`](_examples/player).
For a CGO-free Oto build, run `mise run build:example:oto`. The executable is
written to `var/bin/oto/player` (`player.exe` on Windows):

```text
player -pairing-token
player -output oto -allow-approximate -server ws://192.0.2.1:8927/sendspin
```

Oto requires explicit best-effort opt-in: its timing is estimated and its flush
cannot retract downstream audio. It is not a physical RC1 synchronization claim.
For hardware-clock feedback, install PortAudio headers/libraries and a C compiler,
then use `mise run build:example:portaudio` and `-output portaudio`. This backend
requires CGO automatically; there is no custom PortAudio build tag.

Credentials default to the operating system's user-config directory, not the source
tree. This is an example application, not a production-ready packaged player.
See [backends and their limitations](docs/usage.md#audio-backend-comparison).
Development now also includes an optional [direct Linux ALSA module](docs/linux-alsa.md),
independent of Oto/PortAudio; it is not part of the published v0.1.1 release.
The optional [native desktop module](docs/native-desktop-audio.md) adds direct
PipeWire, WASAPI and CoreAudio outputs; it is also unreleased.

## Develop and test

With [mise](https://mise.jdx.dev/):

```text
mise trust
mise install
mise run check:full
```

`mise run format` and `mise run lint` use the pinned **golangci-lint** v2 configuration,
including tagged SDK sources and formatting of the independent player example.
Project build outputs, test reports and fixture environments live under [`var`](docs/generated-files.md); shared tool caches and system temporary directories keep their defaults.

`mise run test:e2e` starts the pinned **aiosendspin 10.2.1** reference server.
No Music Assistant installation, container runtime, sound card or manual pairing
is required. Python and locked dependencies are provisioned through `uv`.
After provisioning, the tests use loopback sockets only, not LAN discovery.

`mise run test:conformance` additionally runs 15 selected cases from the pinned
official harness using this SDK's own local adapter. See
[scope and evidence](docs/conformance-testing.md); this is not full certification.

Plain `go test ./...` requires neither Python nor PortAudio. See
[development](docs/development.md) and [releasing](docs/releasing.md).
Normal Go tests also replay frozen C++ time-filter vectors without C++ tooling.
The full contributor gate compiles the pinned oracle live and therefore additionally
requires a native GCC/Clang-compatible C++17 compiler (`CXX` can select its path).
It also runs `mise run test:mdns` in a private internal Linux container network;
Podman or Docker is required for that isolated multicast suite, not for ordinary
Go tests or `test:e2e`. See [discovery testing](docs/discovery-testing.md).

`mise run test:soak` performs the longer virtual-output resource campaign (three
minutes per cipher suite by default); [receipts and limits](docs/resource-soak.md)
stay separate from the shorter ordinary E2E run. The [API/release review](docs/api-release-review.md)
records ownership, cancellation, public errors and archive boundaries.

Additional hardware-free work is documented in the [transport/load campaign](docs/network-load-testing.md),
[cryptographic source review](docs/cryptographic-review.md), supplemental
[template inventory](docs/rc1-template-inventory.json), and
[extension contracts](docs/extension-boundaries.md). None is physical conformance evidence.
The silence-only output probe is a separate `_examples/audio-probe` module,
not a command nested inside the player; invoking live output still requires an
explicit opt-in and is not part of hardware-free tests.

## License

[MIT](LICENSE), except the Apache-2.0-derived time-filter implementation;
see [NOTICES](NOTICES.md) and [Apache License 2.0](LICENSES/Apache-2.0.txt).
Sendspin is a trademark of the Open Home Foundation. Protocol implementation
does not grant trademark rights. Opus has its own applicable patent terms.
