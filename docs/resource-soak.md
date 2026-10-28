# Sustained software resource soak

```text
mise run test:soak
```

The default explicit task runs **three minutes per RC1 cipher suite**. Its
race-enabled Go JSON events and private per-suite receipts live under `var/soak`.
`SENDSPIN_SOAK_DURATION` may select 12 seconds through 10 minutes per suite. The
ordinary managed E2E/CI run uses a 12-second workload per suite; a short CI pass is
never relabelled as the longer campaign.

The independent server's public PushStream API produces PCM continuously within
each segment. Native pairing/rekeys and reconnections are deliberately bracketed
by producer-stop/drain/restart: the reference's paired-session pairing policy can
quiesce its stream. This is **not gapless playback through rekey** evidence. The
existing buffered-rekey tests independently check that narrower retention duty.
Transport is transparent here; delay/jitter/bandwidth have their separate campaign.

Connections stay up for at least eleven seconds before a deliberate drop. This
respects Run's existing ten-second stable-session backoff reset; forcing only
short sessions would legitimately grow retry waits to 16/32/60 seconds, not prove
a reconnect defect. Streaming continues during this dwell; retry deadlines and
production backoff are not weakened for the fixture.

The workload completes an initial clock burst and peer-observed readiness before
starting audio, then repeatedly exercises real key changes, persistent identity
and volume, slower cooperative virtual-output writes, reconnection and cleanup.
At least half the requested wall duration must contain actual source-sample work.
All native source samples must be decoded; no media payload is retained in the
diagnostic receipt. It joins owned session/proxy/output workers and checks one
device owner, matching open/close counts and bounded queued wire/decoded bytes.

Receipts include exact source fingerprint, platform/Go version, requested and
actual duration, rounds/rekeys/connections, source/decoded sample counts, observed
queue high-water values, output calls, post-GC heap and goroutine telemetry.
Queued counters exclude the popped/in-flight chunk and backend buffers. A finite
32-MiB post-GC heap-growth allowance catches gross retained allocation; it is not
a zero-allocation promise or an exact universal leak detector. Own-worker joins
are asserted directly; process-global goroutine counts are telemetry, not a brittle
claim about unrelated runtime/HTTP workers.

An ordinary unit stress test also performs 100,000 enqueue/reset operations with
exact queue counters and overflow recovery. No sound device, input, host mixer,
global timer resolution, cache cleanup or host network reconfiguration is used.
No physical speed, latency, correction audibility or DAC synchronization follows.
