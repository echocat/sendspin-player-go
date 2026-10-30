# Go module layout

The SDK, all audio backends, the `audio/native` selector, internal Go tools and
the applications in `_examples` share the root module
`github.com/echocat/sendspin-player-go`. Package import paths are unchanged;
there are no local SDK `replace` directives or independently versioned backends.
Go 1.27.3 or newer is required.

From the repository root:

```sh
go test ./...
go test ./_examples/player ./_examples/external-control
go build ./_examples/player ./_examples/external-control
go run ./_examples/player -h
go run ./_examples/external-control
```

These commands also work with `CGO_ENABLED=0`. The player help command and the
fake external-control demonstration do not open audio devices or use discovery.
Running the player normally is an explicit application action and can open audio
devices and discover servers.

`./...` skips `_examples`. The root `tool` directive registers the player
application so that `go mod tidy` retains its dependencies despite that directory
name; it can also be invoked with `go tool player -h`. The root requirement on
`github.com/echocat/slf4g/native` is for this application's logging provider.
Only the application imports and installs that provider. A module requirement
does not itself import a package, run its initialization or install a provider
in SDK consumers. The SDK uses the `slf4g` facade without choosing an output.

## Distribution and privacy boundaries

Go's `ignore` directives filter wildcard package discovery, not module-zip
contents. The module archive intentionally includes the public examples, all
audio backends and native selector. The archive integration test extracts it,
runs `go test ./...` and explicitly builds both examples.

Two dependency-free nested `go.mod` files are deliberate privacy markers, not
separate Go SDK implementations:

- `internal/e2e/go.mod` excludes Python environments and reference-server assets.
- `internal/conformancetest/go.mod` excludes the Python conformance harness.

`golang.org/x/mod/zip.CreateFromDir` omits nested modules and their contents.
Keep these markers even though the harnesses have no Go dependencies. Generated
assets live under `var`, whose generated module marker similarly excludes them
from directory-based archives; do not remove existing environments or caches to
make an archive pass. `TestModuleArchive` rejects Python environments, either
harness and generated `var` assets in the SDK zip.
