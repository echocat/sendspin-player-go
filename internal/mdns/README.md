# SDK-private scoped mDNS adaptation

Imported from `libp2p/zeroconf v2.2.0`, immutable revision
`586e7c571d106f842c8567012dacd28d73fcf966`. The five generated runtime files and
MIT license are reproduced by `internal/mdnsimportcmd`; each upstream file's
SHA-256 is pinned there. No public SDK type exposes this private package.

Why: the upstream receiver accepted packets from interfaces outside SelectIfaces
and the publisher answered using the receiving interface even when it was not
one of its registered interfaces. On Linux, wildcard multicast sockets can receive
traffic for other joined interfaces. An isolated two-interface test reproduced an
old-interface address being returned to a peer on the new interface.

Changes are deliberately explicit in the importer:

- Require kernel receiving-interface metadata at socket setup; failure is an
  error, never permission to guess a link-local scope.
- Preserve that metadata in the browser and drop foreign/unknown indices before
  parsing DNS records or dispatching publisher queries.
- Route internal diagnostics through slf4g with static, redacted messages.
- Use Go UDP packet-info control messages through a pure-Go Windows adapter,
  because x/net's interface control-message methods are not implemented there.
  Native loopback tests verify IPv4/IPv6 receiving index and scoped replies;
  no multicast or physical interface is opened by those ordinary tests.
- Keep existing DNS-SD records, protocol behavior and pinned dependencies otherwise.

```text
go run ./internal/mdnsimportcmd          # fetch, verify, reproduce, compare only
go run ./internal/mdnsimportcmd --write  # explicit regeneration
```

The ordinary tests stay offline. A failed anchor/hash/comparison is not silently
accepted. Edit scope policy in `scope.go` and transformation logic in the importer,
not generated files. Multicast integration tests run only in the isolated runner.

This is not a complete mDNS reimplementation or proof of every native network
stack. Where kernel packet-info setup fails, use explicit/manual discovery rather
than advertising or assigning an unverifiable scope. Missing, truncated or
ambiguous packet scope is rejected rather than inferred from peer payloads.
