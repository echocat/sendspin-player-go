# Releasing

SDK **v0.1.1** follows the initial API release; the API may change before v1.
Do not describe it as fully RC1-conformant; consult `conformance.md`.

Before a release:

Review the [API/package findings](api-release-review.md), independent
[Noise oracle](noise-vectors.md) and actual-duration [resource soak](resource-soak.md)
alongside the remaining conformance evidence; none authorizes a tag by itself.

1. Run `mise run check:full` on a clean checkout and inspect all CI OS jobs.
2. Build/test the root-module player and fake host-control examples, native
   selector/backends and internal offline test tools. Record native hardware
   results separately from fake/loopback/software-graph evidence.
3. Review the spec, reference-server, decoder and tool pins; include dependency
   changes and remaining conformance limitations in the release notes.
4. Ensure the distributable root module does not contain credentials, virtual
   environments or binaries. The archive test validates
   module boundaries, not every possible sensitive filename.
5. Tag the root module using Go semantic versioning. Do not use the Sendspin
    specification release number as the SDK version by implication.

Before claiming **complete conformance**, also run `mise run audit:check-conformance`
and inspect the [traceability report](traceability.md), source/applicability review
and actual platform/hardware measurements. That strict gate is intentionally
blocked today. A green software-evidence profile is not permission to advertise
full RC1 or physical synchronization conformance. The human clause table is
generated from `rc1-requirements.json`; do not hand-edit it to remove open gaps.

The current tree has one publishable Go module. The Python harness marker
`go.mod` files exist only to exclude harness environments from SDK archives;
they have no Go dependencies or release/version lifecycle. Historical PortAudio
submodule tags remain untouched but are not part of the current backend surface.
No release automation pushes tags or publishes artifacts.
