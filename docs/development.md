# Development

Use [mise](https://mise.jdx.dev/) to install the locked tools, including Go **1.27.3**.
Run these commands from the repository root:

```shell
mise trust
mise install
```

The SDK, audio backends and internal Go tools share the root module.
Each [example](../_examples/README.md) has its own module and a local replacement
pointing to the root SDK; root `go test ./...` does not include those modules.

## Common tasks

| Command | Purpose |
| --- | --- |
| `mise format` | Format Go sources and tidy modules |
| `mise lint` | Check Go, modules, examples and workflows |
| `mise test` | Run ordinary SDK tests and all example-module tests |
| `mise test:e2e` | Run Go integration tests with external services |
| `mise test:fuzz` | Run bounded fuzz campaigns |
| `mise build:examples` | Build examples under `var/bin/examples` |
| `mise example:ls` | List runnable examples |
| `mise example player --help` | Show player options without opening audio |
| `mise check:full` | Run lint, ordinary/E2E tests, fuzzing and vulnerability checks |

List all tasks with `mise tasks`. CI runs the same tasks through mise.

## Tests and dependencies

Ordinary tests run without build tags and need no Python, containers or physical
devices. E2E tests use the single `e2e` tag and `TestE2E...` names; package
`TestMain` owns service setup and cleanup once per package.

Go E2E helpers bootstrap the pinned Python reference environments automatically;
no separate Python setup or manual service startup is needed. Initial bootstrap
needs network access to download tools and dependencies.

Discovery E2E tests need a running Linux Podman or Docker engine; on Windows/macOS,
start its Linux VM first. Multicast stays inside isolated container networks,
not on the host LAN. The optional live time-filter comparison needs a
GCC/Clang-compatible C++17 compiler (`CXX`, or `c++`, `g++`, `clang++`).

Race detection needs a C toolchain: GCC on Linux, Xcode Command Line Tools on
macOS, or MinGW-w64 on Windows. Production native audio does not require a C
compiler or audio development headers; see [audio setup](audio.md) for runtime
libraries and device access.

Optional flags and a focused E2E selection:

```shell
mise test --race
mise test --coverage
mise test:e2e -- -run '^TestE2EReferenceCore'
```

`--coverage` enables race detection and writes SDK coverage to root `profile.cov`
(`profile.e2e.cov` for E2E); it does not collect coverage from the independent
example-module child processes.

## Optional hardware tests

Copy [`.env.example`](../.env.example) to Git-ignored root `.env` and set only the
devices you intend to use. The Go E2E helper reads these four keys as literal
values; existing process variables win, including explicitly empty values.
This file is test configuration, not application or SDK configuration.

| Key | Meaning |
| --- | --- |
| `SENDSPIN_TEST_PLAYBACK_DEVICE` | Explicit native output device; playback only |
| `SENDSPIN_TEST_CAPTURE_DEVICE` | Explicit native input/capture endpoint |
| `SENDSPIN_TEST_AUDIO_BACKEND` | Optional `alsa`, `pipewire`, `wasapi` or `coreaudio` |
| `SENDSPIN_TEST_REFERENCE_WAV` | Optional reference WAV; otherwise generated |

Missing required device values skip the corresponding hardware test before it
opens hardware; invalid supplied configuration fails, without default-device
fallback. Playback does not authorize microphone capture, and loopback needs both
device IDs. Tests never change host mixers, gains, defaults or routing.

## Further reading

[SDK usage](usage.md) · [Audio setup](audio.md) ·
[Examples](../_examples/README.md) · [Releasing](releasing.md)
