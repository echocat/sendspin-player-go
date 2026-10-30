[![Go Reference](https://pkg.go.dev/badge/github.com/echocat/sendspin-player-go.svg)](https://pkg.go.dev/github.com/echocat/sendspin-player-go)
[![Continuous Integration](https://github.com/echocat/sendspin-player-go/actions/workflows/ci.yml/badge.svg)](https://github.com/echocat/sendspin-player-go/actions/workflows/ci.yml)

# sendspin-player-go

A Go SDK for Sendspin players: encrypted connections, audio decoding,
synchronized playback, discovery, and optional audio capture.

## Install

Go **1.27.3+** is required.

```shell
go get github.com/echocat/sendspin-player-go
```

The SDK and native audio backends build without CGO. Opening an audio device
still requires the operating system's runtime libraries and permissions.

## Quick start

```go
package main

import (
    "context"
    "os"
    "os/signal"
    "path/filepath"

    sendspin "github.com/echocat/sendspin-player-go"
    "github.com/echocat/sendspin-player-go/audio/native"
)

func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()

    configDir, err := os.UserConfigDir()
    if err != nil {
        panic(err)
    }
    player, err := sendspin.New(ctx, sendspin.Config{
        Name:   "Living room",
        Store:  sendspin.FileStore{Path: filepath.Join(configDir, "sendspin-player-go", "device.json")},
        Output: native.Output{},
    })
    if err != nil {
        panic(err)
    }
    if err := player.Run(ctx, "ws://192.0.2.1:8927/sendspin"); err != nil && ctx.Err() == nil {
        panic(err)
    }
}
```

Pair the client with your server using `player.PairingToken()`. Treat the token
and credential file as secrets. See [SDK usage](docs/usage.md) for pairing,
discovery, server-initiated connections, and cancellation.

## Features

- Encrypted client- and server-initiated connections with mDNS discovery.
- PCM, FLAC, and Opus decoding, buffering, clock synchronization, and drift correction.
- Native ALSA, PipeWire, WASAPI, and CoreAudio playback and capture.
- Controller commands, metadata, artwork, visualizer, and color updates.
- Optional Source streaming, PIN pairing, and host-managed volume/mute.
- Replaceable audio interfaces, codecs, storage, and application-selected logging.

Targets Sendspin **1.0.0-rc1**. See [protocol support and limitations](docs/conformance.md).

## Documentation

- [SDK usage](docs/usage.md)
- [Audio backends and capture](docs/audio.md)
- [API reference](https://pkg.go.dev/github.com/echocat/sendspin-player-go)
- [Runnable examples](_examples) — `mise example <name> [arguments...]`
- [Development](docs/development.md) · [Releasing](docs/releasing.md)

## License

[MIT](LICENSE), with third-party notices in [NOTICES](NOTICES.md).
The derived time-filter code uses [Apache License 2.0](LICENSES/Apache-2.0.txt).
Sendspin is a trademark of the Open Home Foundation.
