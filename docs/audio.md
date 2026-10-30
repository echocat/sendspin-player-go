# Audio

Select an output for playback and, separately, an input for opt-in Source capture.
All packages below belong to the SDK's root module. Native backends build with
`CGO_ENABLED=0`; no C compiler or audio development headers are required.
Their system libraries, services and device permissions are runtime requirements.

## Choose an output

```go
import "github.com/echocat/sendspin-player-go/audio/native"

// Before sendspin.New:
config.Output = native.Output{}
```

| Package under `audio/` | Platform | Runtime / purpose |
| --- | --- | --- |
| [`native`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio/native) | Linux, Windows, macOS | Platform selector for the backends below. |
| [`pipewire`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio/pipewire) | Linux | libpipewire (0.3.50+) and a usable PipeWire graph. |
| [`alsa`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio/alsa) | Linux | libasound and an available PCM device. |
| [`wasapi`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio/wasapi) | Windows | System WASAPI/COM APIs. |
| [`coreaudio`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio/coreaudio) | macOS | System audio frameworks. |
| [`noop`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio/noop) | Any | Explicit paced discard output; no input or hardware clock. |

Native implementations support amd64 and arm64. Linux auto selection tries
PipeWire first, then ALSA if PipeWire `Open` fails; it does not switch backends
after a stream has opened. Windows selects WASAPI; macOS selects CoreAudio.
Explicit `Backend` selection never falls back to another backend.
An explicit `SharedLibPath` must load that exact library successfully; it also
disables Linux auto fallback. Noop is only selected explicitly, never on failure.

Select hardware with `native.Output.Device`, using a backend-specific identifier:

```go
config.Output = native.Output{
    Backend: native.BackendALSA,
    Device:  "hw:CARD=Device,DEV=0", // Replace with your card identifier.
}
```

PipeWire uses a node name/serial, WASAPI an endpoint ID, and CoreAudio a device UID.
Pin the backend when supplying its identifiers. With no device specified, the
selector uses the backend default (ALSA: `hw`). Direct `alsa.Output{}` instead
selects ALSA `default`; non-hardware plugins require `AllowPluginTiming: true`.
For dedicated Linux players, direct `hw:` selection avoids plugin routing;
the device must accept the requested rate/channels and may already be in use.

Construction is inert. Outputs do not start capture or change system defaults,
mixers or hardware volume. Configure `Config.Formats` for playback combinations
your device supports; defaults use 48 kHz stereo PCM/FLAC. Buffer and lead-time
settings are application-chosen hints, not measured device latency.
See the runnable [player example](../_examples/player) for backend/device options.

## Custom audio I/O

Implement [`audio.Output` and `audio.Device`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio#Device)
to supply another output. `Write` receives interleaved float32 and must copy any
samples retained after return. `Next` reports the predicted audio-port time of
the first frame of the next write, including queued audio and DAC latency,
in Go's monotonic clock domain. Track the device clock, not wall-clock sleeps or
sample counts alone. `Flush` discards queued output and resets the timeline;
successful `Flush`/`Close` must not leave old samples playing.

For capture, implement [`audio.Input` and `audio.InputDevice`](https://pkg.go.dev/github.com/echocat/sendspin-player-go/audio#InputDevice).
`Read` returns a **sample count**, not frames or bytes; it must be a channel
multiple. Its timestamp is the best-effort ADC time of the first sample in Go's
monotonic domain, not read-arrival time. Do not retain the borrowed destination.
Bound capture queues and report lost continuity through `InputDiscontinuityError`.
Open, read and write operations must honor cancellation promptly; the owning
worker closes the input after its final open/read returns.
The linked API contracts define ownership and timing requirements in full.

## Opt-in Source capture

Before `New`, configure both `SourceRole` and an input. For a source-only device:

```go
config.Roles = []sendspin.Role{sendspin.SourceRole}
config.Input = native.Input{Device: captureDevice}
config.SourceFormat = audio.Format{SampleRate: 48000, Channels: 2}
```

`captureDevice` is the selected backend's input identifier; set `Backend` too when
pinning that choice. Include `Player` and an `Output` only if playback is also
needed. Source sends interleaved PCM16 to the server; a zero `SourceFormat`
defaults to 48 kHz stereo.

Configuring an input, constructing a client, connecting, becoming available or
activating the role **never starts capture**. Opening requires a valid new server
start while Source is active, synchronized, available and authorized.
`AllowUnpaired` remains false by default; unpaired Source needs explicit server
approval distinct from playback approval.

Stop, unavailability, role loss and disconnect cancel capture and clear its start
authorization. Reconnection or becoming available again cannot resume recording
without another valid start. Open/read failure also consumes that authorization.
Captured samples never feed the local output. Monitor/loopback capture is a
separate explicit input choice, not an automatic side effect of playback.

For lifecycle and pairing integration see [SDK usage](usage.md); for physical
validation limits see [conformance](conformance.md).
