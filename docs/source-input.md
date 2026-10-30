# Opt-in source@v1 / audio input extension

SDK includes this opt-in extension implementing the **source** role in
the same pinned Sendspin profile, not a private wire protocol and not a new
specification version:

- Specification `1.0.0-rc1`, revision
  `671a34d408581fc25ea56b3528a4a3f13e3be901`.
- Normative source:
  [`roles/source/v1.md`](https://github.com/Sendspin/spec/blob/671a34d408581fc25ea56b3528a4a3f13e3be901/roles/source/v1.md),
  read together with that revision's `messaging.md` and `pairing.md`.
- Independent peer: unchanged **aiosendspin[server]==10.2.1**, locked in
  `internal/e2e/uv.lock`. `source_controls.py` drives native source APIs and
  consumes the native decoded SourceStream; its optional wire probe only observes
  encrypted transport output/order. It does not replace protocol handling.

SDK, core wire version `1` and the role version `source@v1` are separate
version axes; the specification and independent-peer pins above are unchanged.

The frozen primary player-profile inventory and its historical assessments are
**not** enlarged or promoted by these tests. Source extension E2E tests have
explicit supplemental dispositions in `rc1-requirements.json`. Existing historical
outside-scope notes are not a statement about this newly selected source profile.
Full source clause-by-clause audit, independent security acceptance and physical
ADC/platform measurement remain separate work; passing fake-input tests does not
certify hardware capture timing or all RC1 conformance.
Native capture hardware has not been validated; software E2E success is not
physical input/output synchronization evidence.

## API and privacy

```go
client, err := sendspin.New(ctx, sendspin.Config{
    Name: "Line input",
    Store: sendspin.FileStore{Path: credentialPath},
    Roles: []sendspin.Role{sendspin.SourceRole}, // never a default role
    Input: myExplicitInput,                     // implements audio.Input
    SourceFormat: audio.Format{SampleRate: 48000, Channels: 2},
    AllowUnpaired: false, // default; especially important for microphones
    DisableDiscovery: true,
})
```

`SourceFormat` zero defaults to 48 kHz stereo; this initial implementation accepts
8–192 kHz and 1–8 channels. Encoding is signed interleaved little-endian **PCM16**.
The server must offer source support with both baseline codecs `flac` and `pcm`;
unknown codec names are ignored and only offered PCM is announced. No FLAC/Opus
encoding or line-sensing capabilities are advertised.

Import, configuration, `New`, handshake, availability, reconnection and role
activation alone **never** call `Input.Open`. The session opens only after a
server `source.command: start` while the source is active, clock-synchronized,
available and permitted by local unpaired policy. Each start authorizes at most
one opening. Duplicate starts while pending/open are ignored, not remembered.
There is deliberately no local `StartRecording` API or automatic signal-triggered
capture. The server must explicitly approve unpaired **source** access; ordinary
player/playback approval is not a substitute. The SDK does not emulate approval.

`SetAvailable(false)` cancels pending/open capture and sends `client-stream/end`
before reporting unavailable. Stop, source role removal and disconnect also clear
the authorization. Becoming ready/available again, reactivation or reconnection
cannot resume capture without a new start. A new start arriving while an old
canceled Open/Close is finishing waits for that one worker, preventing unbounded
Open goroutines. Open/Read failure consumes the authorization; applications can
observe the bounded best-effort `source_error` event. Errors are not logged with
backend/device details. Applications should not log private backend errors blindly.

**Source vs external-source availability:** `source@v1` captures audio into the
server. Player `available:false` instead reports takeover by a non-Sendspin
playback system. They are different concepts. A combined player/source device
never routes captured samples to its own Output; it plays only server-distributed
audio and uses the connection-wide availability setting.

## Native-input integration hooks

`audio/input.go` defines the backend-neutral contract (no native imports):

```go
type Input interface {
    Open(context.Context, Format) (InputDevice, error)
}
type InputDevice interface {
    Read(context.Context, []float32) (n int, first time.Time, err error)
    Close() error
}
type InputDiscontinuityError interface {
    error
    AudioInputDiscontinuity() bool
}
```

- `n` is a **sample count**, a channel multiple, not a frame/byte count. Samples
  are interleaved float32. The caller owns `dst`; retaining it is prohibited.
- `first` is actual best-effort ADC time of the first returned sample in the local
  Go monotonic `time.Time` domain, not Read arrival or Unix/server microseconds.
  Convert native device/QPC clocks to that domain inside the backend. The Source
  engine inverts the existing filter using both offset **and drift**, then puts
  the resulting server microseconds in the ID-12 eight-byte big-endian timestamp.
- Backends must bound their capture queues and report overruns/timeline resets
  with `InputDiscontinuityError`. Returned samples alongside a discontinuity start
  a new timeline at `first`; old partial blocks are discarded. A large time gap
  also prevents joining unrelated partial reads. No implicit reopen occurs.
- Open and Read must promptly honor cancellation. Close runs once on the owning
  worker after Open/Read return, including late successful Open after cancellation.
  Backend Close must release resources promptly. Arbitrary uncooperative native
  calls cannot safely be preempted by the SDK.
- The engine aggregates ordinary chunks to approximately 20 ms (whole frames),
  far below the 150-ms wire limit. A final partial chunk is allowed. Float samples
  are rounded/clamped to PCM16; NaN becomes silence. The capture queue holds at
  most three chunks (~60 ms), dropping oldest backlog. A separate local monotonic
  enqueue timestamp bounds **application queue wait** to 100 ms after network
  stalls/rekeys. It never replaces the ADC timestamp: a freshly enqueued chunk
  with fixed 150-ms Bluetooth/DSP/input pipeline latency is still sent with its
  original first-sample time. Backend capture-queue bounds and discontinuity
  reporting remain the backend's responsibility; ADC age alone cannot distinguish
  fixed pipeline latency from backend backlog.

The Session actor is the only channel writer. It orders `client-stream/start`,
encrypted ID-12 audio and `client-stream/end` alongside existing writes and
fragmentation. During a Noise re-handshake, application writes pause until
activation; retained same-role capture is not reopened, and stale backlog drops.
Removed or newly unauthorized roles cancel capture. No Output/mixer, CEC, global
network settings, default multicast, audio logging or logger provider is added.

## Targeted evidence (no hardware in CI)

| Behavior | Evidence |
| --- | --- |
| Explicit/inert config and hello codec/support validation | `TestSourceConfigurationIsExplicitAndInert`, `TestSourceHelloRequiresBaselineSupportAndIgnoresUnknownCodecs` |
| Single authorization, duplicate commands, pending/late Open, bounded replacement | `TestSourceStartAudioEndOrderingAndCaptureTimestamp`, `TestSourcePendingStopLateOpenAndNewAuthorizationAreBounded` |
| Stop/unavailable/role loss and no implicit resume | `TestSourceUnavailableRoleRemovalAndReactivationNeverAutostart`, `TestSourceDeniedStartsDoNotOpenOrBecomePending` |
| 20-ms PCM16, ADC timestamp, partial reads/discontinuity/final tail | `TestSourceStartAudioEndOrderingAndCaptureTimestamp`, `TestSourceCaptureDiscontinuityPartialReadsAndFinalChunk` |
| Bounded backlog, invalid backend reads and failure | `TestSourceQueueDropsBacklogAndBoundsMemory`, `TestSourceCaptureInvalidReadsAndOpenFailureAreBounded` |
| Fresh chunks with fixed 150-ms ADC latency retain their old wire timestamp; actual app-queue stalls drop stale chunks and resume fresh capture | `TestSourceFreshChunksWithDelayedADCTimestampsAreSent`, `TestSourceQueueStallDropsOldChunksNotFreshDelayedCapture` |
| Actual encrypted rekey pause, retained open and new-key audio | `TestSourceRekeyPausesWritesAndPreservesOpenWithoutReopening` |
| Drift-inclusive inverse | `internal/timesync/TestSourceInverseIncludesDriftRatherThanOnlyOffset` |
| Independent source decoder, exact stereo PCM, wire order, availability, reconnect, combined player/source without local monitoring | `TestReferenceSourcePCMInputLifecycleAndReconnect` (`-tags=e2e`) |
| Independent explicit unpaired source approval, revoke/reactivate | `TestReferenceSourceRequiresExplicitUnpairedRoleApproval` (`-tags=e2e`) |

Run `go test -race . ./internal/timesync -run TestSource` and
`go test -tags=e2e . -run TestReferenceSource`. The E2E fixture provisions its
managed Python environment under `var/python/e2e`; it uses loopback only. Neither
test suite opens audio hardware. `CGO_ENABLED=0 go build ./...` builds this core.
