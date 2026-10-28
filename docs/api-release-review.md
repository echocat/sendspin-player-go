# Public API and release-package review

This review covers the reusable SDK, not a product certification or permission to
publish a version tag. Specification RC1, wire version 1 and SDK version remain
independent. No tag or binary release is created by the checks below.

## Findings addressed

- `ErrProtocol` is now a public alias of the existing protocol sentinel. A caller
  can use `errors.Is` without importing an internal package. It covers recognized
  shape/direction/sequencing errors; not every transport/authentication or custom
  backend/Store error is collapsed into that category.
- Credentials' record slices are detached at both custom Store boundaries. New
  and Save no longer expose the Client's live records through a Store-owned or
  Store-retained slice. Regressions deliberately use an aliasing Store.
- Package/API documentation distinguishes public ClientID from private identity
  material, ephemeral construction from started workers, listener ownership,
  callback restrictions, lossy events, readiness and external output delay.
- A runnable external-package `ExampleNew` constructs a controller without audio
  or networking and demonstrates the public disconnected error. It uses the
  recording logger; production examples continue to choose native logging.

## Ownership, cancellation and errors

| Surface | Contract / review result |
| --- | --- |
| `New` | Copies mutable configuration, loads or creates/persists identity; starts no network, discovery or output worker. Constructor errors remain errors, not logged credential objects. |
| `Connect` / `Run` | One outbound reservation. Connect is one session; Run retries only the selected recoverable purposes/reasons with backoff. Cancel and join the returned operation; pairing-only loss is not silently resumed. |
| `Serve` / `ServeHTTP` | Serve owns/closes its listener and joins its handlers. ServeHTTP only owns its upgraded connection, not the host HTTP server. Inbound and outbound modes remain exclusive. |
| `DiscoverAndRun` | Chooses the first syntactically usable discovery endpoint; it is not an arbitrary-endpoint reachability probe or general multi-interface failover policy. Browse can be used for application selection. |
| `Events` | One bounded best-effort channel, not broadcast/durable delivery; stays open for Client reuse. Consumers need their own cancellation select. Snapshot/current-artwork reads retain latest state when events drop. |
| commands/settings | Serialized through the session actor, requiring an admitted session/active role. Argument pointers are copied. Validation, disconnected and canceled errors do not promise one universal precedence. |
| `Store` | Atomic full snapshots, one owning Client, cooperative deadlines. Save must not call back into Client methods because the transaction holds the credential lock. Cancellation cannot undo a committed remote/local change. |
| `Output` / `Device` | One render worker owns the device; borrowed samples must be copied if retained. Context cooperation is required. Hardware truth and downstream flush guarantees are backend contracts, not established by a virtual output. |
| observer/codec hooks | Prompt, nonreentrant, potentially concurrent where documented; payload ownership is explicit. Diagnostic hooks are not default logging. |
| logging | Only slf4g facade in runtime SDK; provider selected by the host, optional per-Client injection. Raw peer/backend errors, credentials and media are not default log fields. |

The SDK does not forcibly kill arbitrary user callbacks or native I/O. It offers
no live unpaired-policy setter, Factory Reset implementation, code-pairing UI,
automatic hint calibration or new role/core version under the guise of API polish.

## Release checks and retained limits

- Run `mise run check:full` on a clean checkout, both ordinary example modules,
  optional native-module/callback checks and the existing platform matrix.
- The module archive test extracts and tests the distributable root with CGO
  disabled. Examples, nested PortAudio, runtime artifacts and Python environments
  remain outside the root archive; shared caches/temp directories are untouched.
- Check separate consumer modules without local SDK replacements before a tag.
  The optional PortAudio dependency pin/tag namespace is independent; this task
  does not expand its Windows/macOS CI or open any output device.
- Fixed independent Noise vectors, isolated IPv6/interface hotplug and sustained
  software soak add finite evidence, not physical synchronization or formal
  cryptographic assurance. The existing strict conformance gate stays blocked.

See [releasing](releasing.md), [discovery](discovery-testing.md),
[Noise vectors](noise-vectors.md) and [resource soak](resource-soak.md).
