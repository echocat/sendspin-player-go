# Explicit ten-second ALSA duplex smoke (not a timing measurement)

This **opt-in test-only** harness runs the SDK's direct ALSA output continuously
with digital zero samples while `arecord` captures ten seconds of stereo PCM24
at 48kHz. It is not a capture API/source role in the SDK and is never run by
ordinary tests/CI. The normal ALSA evidence runner explicitly clears its capture
consent flag, even if the invoking environment had one set.

## Preconditions

- Linux/WSL with CGO, libasound development files and `arecord` (ALSA utilities).
- Explicit consent to opening the microphone input and creating a recording.
- A deliberately selected `hw:` device; no default microphone/device fallback.
- **No analog loopback cable connected**. No mixer, volume, mic monitoring or
  audio-service setting is changed by this test. Digital zeros do not rule out
  device startup/shutdown clicks.

The currently inspected PLAY! 3 advertises two USB channels, PCM16/packed PCM24
and 44.1/48kHz capture. Its analog microphone input is not a line-in; two USB
channels do not prove two independent analog inputs. Do not blindly wire the
headphone output back into that input. A proper level/pinout/bias-safe connection
is a separate prerequisite for later nonzero loopback measurements.
An explicitly accepted [unverified direct-cable experiment](play3-low-level-loopback.md)
is a separate manual workflow, never an extension of this zero-only smoke.
Its explicit `--sdk-output` variant can exercise the SDK ALSA backend with the
same bounded weak reference after the analog path is inspected.

## Run from the repository root (Nushell)

```nu
with-env {
    CGO_ENABLED: "1"
    SENDSPIN_ALSA_DUPLEX_CAPTURE: "1"
    SENDSPIN_ALSA_DUPLEX_DEVICE: "hw:CARD=S3,DEV=0"
    SENDSPIN_ALSA_DUPLEX_ARTIFACT_DIR: ($env.PWD | path join "var" "platform-validation" "alsa-duplex")
} {
    mise exec -- go -C audio/alsa test -count=1 -v -timeout=25s -run '^TestHardwareFullDuplexSilenceCapture$' .
}
```

The opt-in flag, explicit hardware device and absolute artifact directory are all
required. `-count=1` prevents reuse of a previous live test result. The output
stream must supply genuine native timing before the recorder starts. Capture
uses exact requested rate/channel/format and disables automatic conversion.
The SDK output negotiates its supported hardware encoding and reports it.

## Private output and teardown

A new `capture-*` directory is created under the selected artifact root (0700
on Unix). `capture.wav`, `arecord.log` and `report.json` are new exclusive files
(0600); existing files cannot be overwritten. `arecord` writes to the already
opened regular file descriptor, never terminal stdout or a permissive filename.
The sample payload is not decoded, printed, uploaded or retained in test logs.

The recorder requests exactly 480000 stereo frames and has an independent
15-second overall process deadline. Output failure cancels/joins capture;
capture completion/failure stops/joins output before device close. A native API
that ignores context still cannot be forcibly preempted safely; the recorder's
separate deadline prevents an indefinite recording while a native call stalls.
Do not increase the test duration or timeout blindly to conceal a failure.

Successful geometry checks require a complete 48kHz/stereo/packed-24-bit WAV
with exactly 480000 frames. Failure retains the private partial file and report
for local diagnosis. No test uploads this directory as a CI artifact.

**Do not post/upload `capture.wav`: it may contain private microphone audio.**
For assistance share terminal status and, if desired, `report.json` only. Review
diagnostics locally before sharing. All artifacts stay under ignored `var/`.

Passing demonstrates that this host/driver/device tolerated simultaneous
zero-only output and bounded input. It says nothing about actual output latency,
capture calibration, channel independence, native flush audibility/silence,
physical synchronization or a calibrated clock. WSL USB/virtualization effects
remain in the path; this does not replace native Linux acceptance.

## Hardware-free coverage

Ordinary tests use fake output/recorder objects and synthetic WAV headers only.
They check start ordering, zero-only writes, cancellation, recorder/output failure,
joining, exclusive/private artifacts, complete/truncated container geometry and
the capture-consent opt-out guard. They never launch `arecord` or open hardware.
