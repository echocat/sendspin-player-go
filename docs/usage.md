# SDK usage

Start with the [README](../README.md) for embedding, or run the
[player example](../_examples/player). This guide covers integration choices;
the [API reference](https://pkg.go.dev/github.com/echocat/sendspin-player-go)
documents individual types and methods. See [audio](audio.md) for device selection
and capture.

## Identity and pairing

One `Client` and persistent `Store` represent one device. `New` copies configuration,
loads credentials or saves a new identity, and starts no network or audio workers.
Use `sendspin.FileStore{Path: credentialPath}`; never share that path between
devices or concurrently running clients. `MemoryStore` is for ephemeral use.
Protect the credential file and backups: Unix files use private permissions;
on Windows, the application must secure the directory ACL.

Transfer `client.PairingToken()` through an intentional local provisioning UI to
the server operator. It is a secret, not a log field or URL parameter.
`AllowUnpaired` defaults to false; enabling it does not replace server approval.

## Choose a connection lifecycle

| Method | Use |
| --- | --- |
| `Connect(ctx, endpoint)` | One outbound session, ending on disconnect or cancellation. |
| `Run(ctx, endpoint)` | Outbound sessions with reconnect backoff for transient failures. |
| `DiscoverAndRun(ctx)` | Select the first discovered server, then reconnect to that endpoint. |
| `Serve(ctx, listener)` | Accept inbound sessions at `/sendspin` and advertise through mDNS. |
| `ServeHTTP` | Mount the client in an application-owned HTTP server; manage advertising yourself. |

For an application-specific server picker, use
[`discovery.Browse`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/discovery#Browse)
and pass the selected endpoint to `Run`. Endpoints use plain `ws://`, for example
`ws://192.0.2.1:8927/sendspin`; Noise provides authenticated encryption.
Direct TLS requests and `wss://` endpoints are rejected.
Inbound and outbound modes cannot run simultaneously. A dropped pairing-only
session requires operator-driven resumption rather than automatic reconnect.

Cancel the lifecycle context to stop, then wait for the running method to return
before releasing application resources. If started in a goroutine, join that
goroutine; cancellation alone is not a completed shutdown. There is no `Client.Close`.
Custom stores, devices and callbacks must honor cancellation promptly.

`Serve` owns its accepted listener and joins its WebSocket handlers at shutdown.
Choose the listen address explicitly; the SDK does not open a port in `New`.
For local-only integration, construct the client with `DisableDiscovery: true`:

```go
listener, err := net.Listen("tcp", "127.0.0.1:8928")
if err != nil {
    return err
}
return client.Serve(ctx, listener)
```

For LAN service, bind the intended interface and allow mDNS as appropriate.
When mounting `ServeHTTP`, cancel the upgraded requests' contexts and join your
handlers too: `http.Server.Shutdown` alone does not stop upgraded WebSockets.

## State and controls

Default roles are player, controller and metadata. Set `Config.Roles` explicitly
for artwork, visualizer, color or Source; include any default roles you still need.
Player requires an `Output`; Source requires an `Input`.

Consume `Events()` as best-effort notifications, then read `Snapshot()` for a
defensive copy of current state. Events can be dropped, and the channel remains
open across sessions: stop consumers through your own context, not a channel close.
`ActiveRoles` and `ActiveStreams` distinguish role activation from an open stream.
Use `CurrentPosition()` for synchronized milliseconds and `CurrentArtwork(channel)`
to recover the currently applied image after a missed notification.

`SetVolume` and `SetMuted` control this player; `Command` controls the server's
group using its current advertised capabilities. `SetAvailable(false)` yields
playback to another system; it does not select or start an audio input.
`SetOutputDelay` persists a post-port delay, clamped to 0–5 seconds; it is not
automatic latency calibration.

## Host-managed volume and mute

`Config.PlayerControl == nil` keeps software volume/mute and their persisted values.
A non-nil configuration, **including `&sendspin.PlayerControlConfig{}`**, selects
immutable unity gain with no software mute or ramping. Without a bound target it
is fixed/no-controls, never a software fallback. This choice does not set hardware
volume to 100, and it cannot be changed during the client's lifetime.

Bind explicit host setters and initial observations with `BindPlayerControl`;
submit actual readback separately with `ReportPlayerControl`. Reports are complete
observations, not patches. Retain the binding returned by each successful report;
old readback must not be applied to a replacement target using its new binding.
`VolumeKnown`/`MutedKnown` distinguish unknown observations from zero/false.
Only independently supported absolute setters with known state are offered.

`SetVolume`/`SetMuted` success means queue acceptance. Even a request marked
`succeeded` in `Snapshot().PlayerControl` means only that the setter returned nil,
not confirmed hardware state. Binding, reports and reconnect never invoke setters.
Callbacks run serially and must honor their context; a setter failure withdraws
controls until a fresh host report. External observations are not persisted.
The host owns mixer/CEC adapters, target selection and appliance policy.

Use the complete [external-control example](../_examples/external-control) for
binding, readback, target replacement and failure recovery without real hardware.

## Optional PIN pairing

`Config.CodePairing == nil` offers PSK/token pairing only. Opt into exactly one
code method; PSK pairing remains available:

```go
config.CodePairing = &sendspin.CodePairingConfig{Static: true}
```

Static mode provisions a persistent eight-digit device PIN. Present
`StaticPairingPIN()` only in a local provisioning UI; a deliberate local gesture
must call `OpenPairingWindow(ctx)` before pairing. Never expose that action through
unauthenticated network commands. `CancelPairing(ctx)` closes the window and aborts.

Alternatively, set `CodePairingConfig.Display` for dynamic six-digit PINs.
The callback must promptly post to the UI, honor its context and not call client
methods. While `PairingDisplay.Active`, replace other content on that display with
`PIN.Grouped()`; restore it when inactive. Never log PINs. A deliberate local
`OpenPairingWindow` action also clears the persistent dynamic-attempt lockout.
See [`CodePairingConfig`](https://pkg.go.dev/github.com/echocat/sendspin-player-go#CodePairingConfig).

Logging uses the slf4g facade; the host chooses its provider or supplies `Config.Logger`.
