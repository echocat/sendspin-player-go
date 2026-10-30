# Releasing

SDK **v0.2.0** includes the native backend/input, optional PIN, Source and
host-managed player-control APIs; the API may change before v1.
Do not describe it as fully RC1-conformant; consult `conformance.md`.
The SDK release version, core wire version `1`, role versions such as
`player@v1` / `source@v1` and the pinned specification revision are independent.

Before a release:

Review the [API/package findings](api-release-review.md), independent
[Noise oracle](noise-vectors.md) and actual-duration [resource soak](resource-soak.md)
alongside the remaining conformance evidence; none authorizes a tag by itself.

1. Run `mise run check:full` on a clean checkout and inspect all CI OS jobs for
   the final release commit. Earlier implementation CI is not final release CI.
2. Build/test each independent example module, plus the root-module native
   selector/backends and internal offline test tools. Record native hardware
   results separately from fake/loopback/software-graph evidence.
3. Review the spec, reference-server, decoder and tool pins; include dependency
   changes and remaining conformance limitations in the release notes.
4. Ensure the distributable root module does not contain credentials, virtual
   environments or binaries. The archive test validates
   module boundaries, not every possible sensitive filename.
5. Validate an independent consumer against the intended v0.2.0 module artifact,
   without a local `replace` or workspace override. Check the new APIs and native
   package imports, not just the checkout examples. Once published, verify normal
   `@v0.2.0` resolution as well; local archive success alone is not publication.
6. Tag the root module using Go semantic versioning only after the required
   checks and explicit publication authorization. Do not use the Sendspin
   specification release number as the SDK version by implication.

Before claiming **complete conformance**, also run `mise run audit:check-conformance`
and inspect the [traceability report](traceability.md), source/applicability review
and actual platform/hardware measurements. That strict gate is intentionally
blocked today. A green software-evidence profile is not permission to advertise
full RC1 or physical synchronization conformance. The human clause table is
generated from `rc1-requirements.json`; do not hand-edit it to remove open gaps.
Keep Go package documentation and release notes conservative: strict conformance
is not established, hardware is not certified, and native capture hardware remains
unvalidated.

The SDK, all backends, selector and internal Go tools have one root module/version.
Each real example has its own `go.mod`/`go.sum`, requiring SDK `v0.2.0` with a local
`replace` to `../..` for checkout development, not as a prerequisite for the
v0.2.0 APIs. Published v0.1.1 does not provide the new APIs. The SDK uses only the
slf4g base facade; concrete logging providers belong to applications.
The local archive check explicitly excludes examples and Python harnesses from
Git-versioned source selection and rejects tracked `var` data; it needs no
harness/`var` module markers. Historical PortAudio submodule tags remain untouched
but are not part of the current backend surface. No release automation pushes
tags or publishes artifacts.
