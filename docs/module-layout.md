# Go module layout

The SDK, all audio backends, the `audio/native` selector and internal Go tools
share the root module `github.com/echocat/sendspin-player-go`, with no nested
modules under those packages. Package import paths are unchanged; backends and
selector have no independent module/version. Go 1.27.3 or newer is required.

Each real application, `_examples/player` and `_examples/external-control`, has
its own `go.mod` and `go.sum`. Both require SDK `v0.1.1` and locally replace it
with `../..`. The replacement is necessary for the unreleased APIs: the published
`v0.1.1` does not contain them. Neither example requires a separate native module.

From the repository root:

```sh
go test ./...
go -C _examples/player test ./...
go -C _examples/external-control test ./...
go -C _examples/player run . -h
go -C _examples/external-control run .
```

These commands also work with `CGO_ENABLED=0`. The player help command and the
fake external-control demonstration do not open audio devices or use discovery.
Running the player normally is an explicit application action and can open audio
devices and discover servers.

Root `./...` does not cover the example modules. Run Go commands in each module,
or use `mise run test:examples` and `mise run build:example`. Formatting, tidying
and linting also run separately per module. The player is not a root Go tool;
its `github.com/echocat/slf4g/native` provider dependency belongs to the player
module alone. The SDK uses the `slf4g` facade without choosing an output.
The offline WAV/analysis tool remains at `internal/loopbackcmd` in the root module.

## Distribution and privacy boundaries

Go's `ignore` directives filter wildcard package discovery, not module-zip
contents. The root module archive includes all audio backends, the native
selector and internal Go tools, not the separate example modules. The archive
integration test extracts it and runs `go test ./...` with CGO disabled.

The local archive integration test selects only Git-versioned files, explicitly
excludes `_examples`, `internal/e2e` and `internal/conformancetest`, and passes the
remaining files to `golang.org/x/mod/zip.Create`. The Python harness directories
have no `go.mod` privacy markers. Git-ignored and untracked generated data is
neither read nor archived.

Generated assets live under Git-ignored `var`; its root `go.mod` ignore entry
excludes package discovery, so no generated `var/go.mod` is needed. Accidentally
version-controlled `var` data is rejected rather than packaged. Existing outputs,
environments and user caches are never deleted to make an archive pass.
