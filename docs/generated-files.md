# Project outputs and system-managed data

The root `.gitignore` contains exactly `/var/`. Use it for generated files that
belong with this checkout, not as a replacement for system temporary directories
or shared development caches.

| Project path | Why it belongs here |
| --- | --- |
| `var/bin` | Explicit example builds, including `.exe` on Windows |
| `var/coverage` | Coverage reports for this checkout |
| `var/audit` | Generated traceability reports and provenance/result evidence; not a tool cache |
| `var/conformance/run-*` | Inspectable per-run reports, logs and audio fixtures |
| `var/mdns` | Inspectable isolated multicast build/test logs |
| `var/python/e2e` | The locked reference server's project-specific environment |
| `var/python/conformance` | The separately locked official-harness environment |
| `var/python/pycache` | Fixture bytecode kept out of source directories, alongside those environments |

## Local development

Go, golangci-lint and uv keep their normal shared caches. Tasks and fixture
runners do not set `GOCACHE`, `GOMODCACHE`, `GOLANGCI_LINT_CACHE` or `UV_CACHE_DIR`.
They also leave `GOTMPDIR`, `TMPDIR`, `TEMP` and `TMP` alone. User-supplied values
are preserved; otherwise tools use their system defaults. Compiler workspaces,
temporary test executables, test credentials, extracted archives, conformance
adapters and sanitized container contexts do not need to be indexed by the IDE
and use ordinary system temporary directories.

`mise run prepare` is a genuine file task: mise discovers `prepare.sh` on POSIX
and its `prepare.ps1` sibling on Windows. There is no duplicate TOML command that
calls these scripts. Only coverage and explicit example-build tasks depend on
this directory preparation; ordinary formatting, linting and tests do not.
Fixture runners prepare their own project paths when needed.

The root `go.mod` ignores `var` for recursive Go package discovery; underscore
directories such as `_examples` are not discovered recursively either.
Formatting invokes golangci-lint directly, with the independent example checked
from its own module directory. The generated `var/go.mod` boundary separately
keeps project outputs out of archives created by `golang.org/x/mod/zip`.

## CI and containers

The Go-version CI matrix prepares only its coverage destination, without changing
the Go version selected by setup-go. Runner/tool cache management remains with
the respective setup actions and tools; no project-wide local cache policy is
imposed for CI's sake. The other CI jobs use the same targeted output paths as
local tasks. Container fixtures have their own binary and Python-environment
paths, use normal container temporary directories and never mount host var.

## Boundaries

Manual `go test -c` / `go build` commands that create retained binaries need an
explicit `-o var/...`; use `mise run build:example` for the native example.
Checked-in fuzz seeds and frozen reference vectors are intentional source
fixtures. Production FileStore credentials belong at the application's configured
path, not in var; their atomic replacement temporaries must be beside them.

Existing reports remain available. The mistakenly project-local tool caches,
temporary directories and obsolete environment copies from the earlier layout
migration have been removed. Unknown files, notably `.idea`, are not rewritten
or published.
