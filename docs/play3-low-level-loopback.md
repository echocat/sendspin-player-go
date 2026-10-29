# Explicit PLAY! 3 direct-cable experiment (unverified wiring)

This optional, interactive Linux/WSL script is not a normal mise task and never
part of CI. By default it is **not an SDK test**: it uses ALSA utilities to check whether a weak
analog reference can traverse an explicitly accepted, **unverified** direct
headphone-output → microphone-input cable. It does not establish that this
connection is electrically suitable.

Creative documents a headphone/headset output and a separate microphone input,
not a line input: <https://us.creative.com/p/sound-cards/sound-blaster-play-3>.
That product page does not establish input pinout, bias voltage, DC protection
or admissible input levels. Muting or reducing AC audio does **not** remove DC
bias. Neither user consent nor a successful recording proves electrical safety.
A verified attenuating/DC-isolating connection remains preferable; this script
exists only for the explicitly requested experiment with the available cable.

## Preconditions and command (Nushell)

- Only the previously inspected PLAY! 3, card ID `S3`, PCM `hw:CARD=S3,DEV=0`.
- A straight ordinary stereo **TRS-to-TRS 3.5mm cable**, two insulating rings on
  each plug. No mono/TRRS cable, splitters, summed L/R wiring or connected
  headphones/speakers. Keep both ends disconnected initially.
- Close other audio applications using this card; the script cannot prevent
  another process changing hardware mixer controls during playback.
- Explicit acceptance of unresolved electrical risk, temporary mixer changes,
  a weak test signal and five seconds of private microphone recording.
- ALSA utilities, GNU `timeout`, Bash, Go via mise; an interactive terminal.
  No sudo, global configuration, default device changes or background services.

From the repository root:

```nu
mise exec -- bash mise-tasks/play3-low-level-loopback.sh --acknowledge-unverified-wiring
```

The script is Bash, deliberately launched explicitly from Nushell. It first
requires `VORBEREITEN` with the cable disconnected and checked. It builds the
existing offline tool and generates a three-second 48kHz/stereo PCM16 reference
before changing mixer values. Only the left channel contains three short marker
bursts, peak digital amplitude 0.01 (−40dBFS); the right channel is digital zero.
This does not leave the right output electrically disconnected.

It saves card S3's mixer state into a new private directory, mutes Speaker and
Mic playback/monitor, requests Speaker −40dB and Mic capture 0dB, enables capture,
and verifies the two reported channels, dB levels and switches. It aborts rather
than guessing if readback is incompatible. Requested values may be rounded down
by hardware, not above those bounds. These dB values are mixer settings, **not
calibrated voltage limits**; they do not guarantee a safe analog connection.
The card must report USB component ID `USB041e:324d`; a mismatch aborts without
changing mixer values. The same product check is required before restoration.

Only then does it ask you to connect the headphone output to the **separate
microphone jack** and type `VERBUNDEN`. Mixer state is rechecked. A five-second
PCM16 capture starts, followed by the three-second reference via `aplay`.
Capture/playback are process-time-limited (7s/5s plus at most 1s kill grace).
There is no retry or automatic gain increase. Timeout, xrun and command failures
abort the run. Startup/shutdown clicks are not excluded.

### Separately consented +24dB capture comparison

The default remains capture 0dB. After a separately authorized comparison, use:

```nu
mise exec -- bash mise-tasks/play3-low-level-loopback.sh --acknowledge-unverified-wiring --capture-gain-24db
```

This requests **exactly +24dB on both capture channels**, aborting if readback
differs. Only capture gain changes: the Speaker bound (−40dB), left-only reference
(peak 0.01, three seconds), five-second recording, monitoring mute, timeouts and
disconnect-before-restore procedure are unchanged. The selection is displayed
before confirmation and retained in `run-settings.txt`. No arbitrary gain value,
automatic escalation or replay is supported. The +24dB setting was the user's
original capture setting; it does not establish input safety or signal presence.

### SDK ALSA output comparison (same levels and waveform)

After the signal path has been inspected, the explicit SDK variant replaces
`aplay` with the SDK's direct ALSA `Next`/`Write`/`Flush`/`Close` implementation:

```nu
mise exec -- bash mise-tasks/play3-low-level-loopback.sh --acknowledge-unverified-wiring --capture-gain-24db --sdk-output
```

It builds a test binary **before any mixer changes**, selects only
`TestHardwareWeakReferencePlayback`, then uses the same recording, gain checks,
timeouts, private artifact policy and disconnect-before-restore workflow. SDK
configuration/results are retained in `sdk-playback.log` and printed after a
successful run. There is no fallback to `aplay`. An SDK failure stops capture and
enters the existing cleanup path. The input loader rejects other WAV layouts,
rates, lengths, nonzero right-channel samples, peaks above 0.01 plus PCM16
rounding, and a missing 200ms silent tail. It submits all reference frames then
flushes; pending silence may be dropped but the final marker precedes that tail.

The native entry point is separately consent-gated and skipped by ordinary
tests. The evidence runner clears that gate even if inherited from the caller.
Fake-device tests cover reference validation and submission without hardware.
This is a **backend signal/lifecycle test**, not a Sendspin server/session,
network-clock, drift-correction or calibrated synchronization acceptance test.

## Disconnect before restoration

On success, failure or ordinary Ctrl-C/TERM/HUP, the script stops and waits for
the audio processes, mutes Speaker and Mic playback where possible and asks you
to **physically disconnect both cable ends**. Only `ABGEZOGEN` authorizes restoring
the saved card state. Do not close the terminal: signal handling cannot survive
SIGKILL, loss of the terminal, USB removal, a driver hang or power loss.

If disconnect confirmation is missing or restoration fails, the script does not
claim successful restoration; it prints the exact manual restore command. After
physically disconnecting, that command has the form (replace the path):

```nu
alsactl --no-lock --no-init-fallback --pedantic --file "/absolute/path/to/run-XXXXXXXX/mixer-before.state" restore S3
```

It restores only the selected card, with initialization fallback disabled. Never
restore a snapshot onto a different device. The script retains before/test/after
mixer text and cleanup diagnostics, and requires matching simple-mixer readback
before reporting successful restoration. On a partial restore failure it attempts to
mute both playback paths again. If the device has disappeared, neither muting nor
restoration can be guaranteed; reconnect it with the analog cable disconnected.

## Results and limits

Each run has a new 0700 directory under `var/platform-validation/play3-low-level`;
files are private via umask 077. Do **not** upload `capture.wav`, the whole directory
or its executable. Review/share terminal status, mixer text or offline
`analysis-channel0.json`/`analysis-channel1.json` as needed.

The existing offline analyzer runs separately for both capture channels. A weak
or absent result is inconclusive; the script never raises levels to obtain a
pass. An aligned recording supports only signal-path/file-alignment observations.
The default `aplay` variant is not an SDK playback test. Neither variant proves
channel independence, absolute latency,
calibrated drift, physical synchronization or electrical safety measurement.
WSL USB/virtualization effects and potentially shared ADC/DAC clocks remain.

## Hardware-free harness regression tests

```text
python3 mise-tasks/tests/test_play3_low_level_loopback.py
```

These Linux PTY tests run a temporary copy of the script with **all** mixer,
audio and Go commands replaced by fakes. They cover consent, readback, a partial
setup failure, capture/playback/xrun failures, ordinary Ctrl-C, disconnect-before-
restore ordering, failed/missing restoration and SDK-versus-aplay selection.
They never touch host audio.
