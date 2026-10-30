# Offline WAV analysis

Internal tool for generating marker WAVs and aligning an existing recording.
It never opens audio devices or plays or records sound.

```text
mise build:loopback
go run ./internal/loopbackcmd generate -h
go run ./internal/loopbackcmd analyze -h
```

Create the output directory first, then generate a reference and its manifest:

```text
go run ./internal/loopbackcmd generate -out var/loopback/reference.wav -seconds 20
```

Analyze a separately obtained recording:

```text
go run ./internal/loopbackcmd analyze -reference var/loopback/reference.wav -manifest var/loopback/reference.wav.json -capture var/loopback/recording.wav -capture-channel 0 -source recording -out var/loopback/analysis.json
```

The report contains marker correlation, relative file offset, sample-coordinate
ratio and residuals. File alignment does not measure absolute hardware latency
or prove physical synchronization. Existing outputs are not overwritten.

The command delegates to `internal/loopback.Run`; its parser and analysis tests
run with the normal Go tests.
