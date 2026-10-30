# Internal offline loopback preparation

A dependency-free, pure-Go internal **test preparation file tool**, not a public
example, audio backend or recorder. It
generates a reference WAV/manifest and aligns a separately supplied WAV against
it. No command opens an audio device, plays a sound, records input, changes a
mixer/volume, loads a native audio library, starts a server or uses the network.

## Run

From the repository root:

```text
CGO_ENABLED=0 go test ./internal/loopbackcmd
CGO_ENABLED=0 go run ./internal/loopbackcmd generate -h
CGO_ENABLED=0 go run ./internal/loopbackcmd analyze -h
mise run test:loopback
mise run test:loopback:artifacts
mise run build:loopback
```

The artifact task retains a clearly synthetic reference, simulated recording,
expected offset/clock-ratio values and report under `var/loopback/synthetic-*`.
The test expects **900 frames file offset and +100 ppm relative sample ratio**;
it is not a device test. The executable is `var/bin/loopback/loopback`
(`loopback.exe` on Windows). No CGO, Oto, PortAudio or SDK runtime dependency.

Generate a new reference (the output directory must already exist):

```text
var/bin/loopback/loopback generate -out var/loopback/reference.wav -seconds 20
```

Defaults: 48kHz, PCM16, stereo with **only channel 0 active**, peak digital
amplitude 0.05, unique deterministic pseudorandom marker bursts, silence between
bursts and after the last burst. The WAV is accompanied by `reference.wav.json`
with its SHA-256, channel, seed and exact marker positions. These are test-signal
bytes written to a file; generation does **not** play them.

After a separately approved, safe recording has been made:

```text
var/bin/loopback/loopback analyze -reference var/loopback/reference.wav -manifest var/loopback/reference.wav.json -capture var/loopback/recording.wav -capture-channel 0 -source recording -out var/loopback/analysis.json
```

Input names are explicit, channels are selected without downmixing, and existing
outputs are never overwritten. The report can instead be written to stdout by
omitting `-out`. `generate -h` and `analyze -h` describe bounded search/options.
There is deliberately **no play/capture/auto-level command**.

## What is measured

- Signed, normalized marker correlation accepts fixed gain, DC offset and
  inverted polarity. A refined peak gives a sub-sample *estimate*, not a calibrated
  physical accuracy guarantee. Competing distinct peaks are marked ambiguous.
- An affine fit reports **relative file-origin offset**, observed capture-to-
  reference sample-coordinate ratio in ppm, residuals and five-marker windows.
  Marker centers are fitted in both files so stretching a finite marker does
  not get mistaken for an additional origin offset.
  Positive ppm means marker spacing occupies more capture samples than reference
  samples, not necessarily that a particular hardware output is faster/slower.
- Missing/ambiguous markers, full-scale digital samples, changing polarity,
  insufficient baseline or nonlinear timing make the result inconclusive.
- Tail RMS/AC RMS/DC/peak describes recording energy after the mapped last
  reference burst plus a 5ms guard. It does **not** measure native Flush/Stop
  completion: that needs a recorded stop event and calibrated clock mapping.

An `aligned` result/zero exit status means the **offline alignment diagnostics**
passed. `physical_conformance` and `hardware_opened` are always false;
`absolute_output_latency_ms` remains null. Inconclusive/invalid analysis returns
a nonzero status, retaining the diagnostic report when possible. The configured
linearity limit is not an RC1 acceptance threshold. Source provenance is a caller
claim (`unknown`/`synthetic`/`recording`), not authenticated by a WAV container.

### What cannot be inferred

WAV origins do not tell us when playback versus capture physically started.
Thus file offset is **not** absolute output latency, nor automatically a valid
combined DAC/ADC latency. A shared ADC/DAC oscillator can also cancel common clock
error in this relative measurement. Absolute rate/synchronization needs an
independent/common-clock reference and capture-latency calibration, plus the
SDK/server target timestamps. Physical +/-1ms synchronization, sliding device
speed, startup audibility, actual native flush, hotplug and output quality remain
separate hardware acceptance work.

For the Creative PLAY! 3 specifically, its input is a **microphone input, not a
true line-in**. Do not join left/right headphone outputs or blindly connect a
normal output level to it. Pinout, attenuation, input bias and levels must be
checked with a suitable measurement connection before any live signal/capture.
The generated low digital amplitude is **not** a guarantee of safe analog level.
No driver support or device behavior has been inferred from these file tests.
The separately consent-gated [PLAY! 3 direct-cable experiment](../../docs/play3-low-level-loopback.md)
documents an explicitly unverified low-level attempt, its remaining electrical
risks and disconnect-before-restore procedure. It is not part of this offline tool.

## Supported and bounded inputs

- RIFF/WAVE PCM16, PCM24, PCM32 and IEEE float32, including supported extensible
  PCM/float GUIDs. Valid PCM bits in larger containers must be left-aligned.
- Reference/capture nominal sample rates must match (8..192kHz). No automatic
  rate conversion, compressed codecs, RF64 or RIFX handling is hidden here.
- At most 64 MiB per input, 16 Mi frames per selected channel and eight channels.
  Only the selected channel is decoded. Invalid/truncated/duplicate format/data,
  bad chunk extents, nonfinite samples and invalid padding bits are rejected.
- Reference generation: 3..120 seconds, peak amplitude in (0,0.1], up to 120
  markers. Selected-channel memory and correlation work have fixed bounds.
- Search is coarse/refined normalized correlation with cancellation checks and
  a 250-million sample-product budget. Narrow `-search-ms` when file origins
  are already known; a budget failure is not silently treated as success.

The command belongs to the repository's root Go module under `internal/loopbackcmd`;
it is not a standalone module or public SDK API. Its synthetic known-answer,
missing/duplicate marker, drift/discontinuity, tail, channel, parser and CLI
no-overwrite tests run as part of the cross-platform CGO-free unit suite.
They provide software-analysis evidence only, not physical conformance credit.
