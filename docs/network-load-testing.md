# Bounded transport and load campaign

`TestReferenceImpairedTransportRecoveryAndCancellation` runs 12 real independent
`aiosendspin==10.2.1` sessions: both WebSocket directions, both RC1 cipher suites,
and PCM/FLAC/Opus. The SDK decodes into a virtual clocked output, never hardware.

`internal/netfault` accepts only explicit loopback IP targets and binds loopback.
It transparently forwards encrypted TCP bytes with bounded read buffers, fixed
periodic delay/jitter, bandwidth pacing and byte segmentation. It never decrypts,
records media/keys, configures network interfaces/firewalls, requests elevated
capabilities or emits multicast. Chunking is TCP read/write segmentation, not a
claim about IP packet reordering/loss. Abrupt close and stalled delivery are
separate fault modes; TCP's ordered/reliable semantics are not weakened.

Each scenario checks player clock-readiness/availability, actual codec decoding,
an abrupt connection interruption, reconnection with the same trust/volume,
cancellation during stalled transport and joined fixture/session workers. The
client owns outbound retry/backoff; inbound reconnection is explicitly requested
from the independent server after a disconnect barrier. Structured recording
logs are checked for private identifiers, endpoints and secrets only after the
session worker joins. Buffer-size and safety-profile regressions are ordinary
unit tests; all race-enabled E2E assertions remain in the managed E2E suite.

The out-of-band HTTP stream command waits for **peer-observed** availability,
not only the SDK's local Snapshot. Otherwise the control request can overtake
the paced `available=true` state and ask the reference to stream while it still
considers the player unavailable. The complete decoded-sample assertion remains
unchanged; no timeout or packet-loss allowance masks that fixture ordering race.

```text
mise run test:network
```

The task repeats the campaign three times with the race detector and retains
its JSON test events under `var/network-load`. The full gate/CI executes it once
through the E2E evidence runner, with full source/catalog/toolchain fingerprints.
Any failed attempt is retained, not imported as a pass. The first development
run exposed a test-only log-inspection race; joining the writer before inspecting
recorded events fixes the actual ownership issue rather than hiding race checks.

This is finite userspace transport evidence. It does not establish arbitrary
LAN/Wi-Fi delivery, native scoped IPv6/interface changes, external server-clock
truth, DAC synchronization, sliding-window physical device speed or audibility.
The existing measurement/platform backlog remains open with those distinctions.
