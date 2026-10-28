# Dynamic mDNS publication (SDK v0.1.1)

This change does not move the published v0.1.0 tags or expand PortAudio CI.
The public constructors remain usable with their existing keyed fields.

## Listener and caller boundaries

`AdvertiseClient` snapshots eligible interfaces/addresses once per second and
reconciles per-interface registrations. Changed/removed bindings are withdrawn
before replacements are published; unchanged bindings are left alone. A local
snapshot failure withdraws the unverifiable registrations and retries. Setup is
still synchronous: an initial failure rolls back every partial registration.

`Client.Serve` preserves the actual listener constraints rather than freezing the
startup NIC list:

- IPv4 wildcard: future IPv4 addresses only.
- IPv6-only wildcard: future IPv6 addresses only, using the socket's actual
  `IPV6_V6ONLY` state rather than assuming a dual-stack socket.
- Dual-stack wildcard: both supported families.
- Concrete IP: that exact address, not other addresses appearing later.
- Explicit IPv6 zone: that index/name interface identity, not an unrelated NIC.

Direct `ClientService.Interfaces`/`Addresses` are immutable restrictions. The
optional `IPVersion` field is 0 (both, preserving the old default), 4 or 6.
Mutable address/hardware-address slices are copied. Refresh does not reopen or
migrate the TCP listener, change its port, rotate ClientID or reserve outbound mode.

## Failure and lifetime contract

`Advertisement.Errors()` is a bounded best-effort error stream;
`Advertisement.Err()` reports the latest refresh failure or nil after recovery.
Nil is not a remote delivery/readiness promise. Errors do not block publication.
Default slf4g health messages are static/redacted and repeated identical failures
do not flood the log. `Client.Serve` exposes later failures as best-effort
`discovery_error` events without shutting down unrelated active TCP sessions.

Close is nil/zero-safe, concurrent/idempotent, cancels the watcher and joins all
owned registrations. A late registration returned after cancellation is closed,
not published. Callers must still provide prompt logging sinks. Goodbyes are
best effort on reachable links; a peer on a lost link can only expire old DNS
cache records by TTL. There is no hard-real-time or instant remote-cache guarantee.

## Scoped transport correction and independent evidence

The test exposed an upstream v2.2.0 bug: packets from an unselected interface
could be processed and answered on that interface with a different binding's
address data. The SDK-private [pinned adaptation](../internal/mdns/README.md)
filters kernel receiving indices before DNS parsing/query dispatch and includes
a pure-Go Windows packet-info adapter. No unsafe reflection, global socket
configuration, daemon socket in a fixture or physical-device operation is used.

The isolated hotplug test keeps existing IPv4-only and IPv6-only wildcard
listeners open, attaches a second internal network, and verifies the same ports
and only their newly reachable address families via independent Python peers.
The IPv6 observer runs in a separate namespace attached only to the new link,
so a multi-interface observer cache cannot collapse two per-link SRV/AAAA domains.
Removal must release obsolete UDP sockets, and final Close must restore the
process's initial socket count. Ordinary mock tests cover exact withdrawal order,
unchanged bindings, retry/recovery, startup rollback, mutable inputs and close races.

These are finite Linux namespace and native Windows loopback results, not full
native LAN/hotplug behavior on every platform. Remaining platform/network and
physical conformance obligations stay open.

The raw-source import has explicit checks:

```text
mise run audit:verify-mdns
```

The full gate/traceability CI verifies the source hashes and exact generated
transformations. Normal tests remain offline. This adds no PortAudio CI job and
does not mutate the already published v0.1.0 source or module tags.
