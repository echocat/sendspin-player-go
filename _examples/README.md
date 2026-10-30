# Examples

Run from the repository root with `mise example <name> [arguments...]`.
Start with the [development setup](../docs/development.md) to install the tools.

| Example | Shows |
| --- | --- |
| [player](player) | Native audio output, discovery or an explicit Noise-encrypted endpoint, and persistent credentials |
| [external-control](external-control) | A fake host adapter for volume/mute, independent readback and target replacement; no hardware |

```shell
mise example:ls
mise example player --help
mise example external-control
```

Each example's README describes its behavior and options. The player opens real
audio during playback; the external-control example uses fake devices only.

Each example has its own `go.mod` with a local replacement for the root SDK.
The dispatcher runs it in that module; `mise test` includes all example modules.
See the [SDK API](https://pkg.go.dev/github.com/echocat/sendspin-player-go) and
[root README](../README.md) for embedding the SDK in your own application.
