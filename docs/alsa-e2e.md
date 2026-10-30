# Bounded independent-server playback through SDK ALSA

This explicit live test combines the pinned **aiosendspin[server]==10.2.1**
server with the Go SDK's encrypted session, clock mapping, PCM/FLAC decoding,
buffering/scheduling and real ALSA output. The external server library is not
patched; `internal/e2e/weak_playback.py` only drives its public streaming APIs.
No mDNS/multicast, host network changes or default audio routing is involved.

## Live command (Linux/WSL, Nushell)

Use the already inspected PLAY! 3 (`S3`, `041e:324d`) and the explicitly accepted
preconnected stereo TRS cable from headphone-out to the separate microphone jack:

```nu
mise run test:alsa:e2e
```

There are no confirmation prompts. The existing connection has unverified
electrical suitability; the [loopback limitations](play3-low-level-loopback.md)
still apply. This task is never a default-test/CI dependency. Invocation explicitly
authorizes weak reference playback but **does not open the microphone input**.

Close other applications using this card. Go/CGO, ALSA development files, uv,
Bash and the usual ALSA mixer utilities are required. The locked Python
environment and Go binaries are prepared before mixer changes. The original
card state is saved privately. Speaker remains at −40dB, Mic capture at the
previous +24dB, monitor off; there is no increase of either test level. The
same three-second left-only peak-0.01 reference is repeated by the producer.
The output tap rejects larger/nonfinite samples or a nonzero right channel
before forwarding them to ALSA. It is a guard, not a substitute audio renderer.

## Scenario

1. Establish authenticated encrypted SDK → server connection on an ephemeral
   localhost TCP port. Discovery is disabled. Fresh ephemeral credentials are
   passed through a private config file, never command arguments or normal logs.
2. Play PCM for 60 seconds; stop the stream and confirm SDK Flush handling.
3. Pause for two seconds; start a fresh PCM stream for 30 seconds.
4. Stop the producer/stream, deliberately disconnect the transport, and require
   automatic SDK reconnection to a second encrypted session. No global network
   fault injection or firewall change is used.
5. Play PCM for 30 seconds after reconnect.
6. Request FLAC through the SDK, wait for the independent server's format
   selection, then play FLAC for 60 seconds and stop.
7. Cancel/join the SDK session; require output opens/closes to balance and no
   unexpected native operation errors. The shell stops all child processes and
   leaves Speaker and Mic monitoring muted, with test settings retained.

Playback source time is about three minutes, plus setup, pause and reconnect.
The SDK scenario has a 240s context limit, the test binary a 250s limit, and its
process a 260s timeout plus a 1s kill grace. No retry of the whole experiment or
fallback to virtual output/aplay occurs. Ordinary cancellation, SIGKILL, USB loss
and driver-stall limitations remain as documented for the loopback harness.

## Evidence

Each private `var/platform-validation/play3-low-level/run-*` has `sdk-e2e.log`,
`server.log`, `run-settings.txt` and `e2e-report.json`. The receipt records the
scenario phases, produced/decoded/submitted frame counts, nonzero writes,
authenticated sessions, native configuration, Flush/Close counts and failures.
`capture_opened` is false; **no new microphone WAV is created**. Do not upload
the entire directory: `server-private.json` contains the ephemeral PSK.

Source and output frame counts need not be identical: stream stops intentionally
discard pending audio, FLAC has finite block buffering, and playback drift
correction adjusts submitted samples. Health checks require continued output
progress, decoded/signal progress in every phase, exactly two sessions, stop
handling and successful teardown. They are bounded campaign checks, not proof
that every source sample was audible or that the system cannot ever underrun.

A pass supports this host's **end-to-end functional playback**. It does not
calibrate physical latency/synchronization or prove analog silence at Stop/Flush,
click-free operation, right-output routing, Opus hardware playback, native-Linux
acceptance or electrical safety. The previously observed start/end transients
remain a separate hardware observation. No physical-conformance promotion occurs.

## Hardware-free validation

```nu
mise run test:alsa:e2e:virtual
```

This uses the same independent server and scenario with four three-second
segments, an explicitly selected virtual clock output and CGO disabled. It never
runs the mixer harness or opens an audio device. Its receipt is marked
`virtual_output: true`. It is not a fallback for a failed hardware run.
Ordinary ALSA evidence clears both scenario opt-ins and does not start a server.
The separate fake shell tests verify E2E command selection, preparation-before-
mixer order, no recorder invocation and final muting on failure.
