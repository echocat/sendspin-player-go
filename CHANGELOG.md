# Changelog

## v0.1.1

- Refresh mDNS advertisements after interface/address changes, withdrawing stale
  bindings while preserving unchanged registrations and listener restrictions.
- Fix cross-interface mDNS query/record processing with a private, source-pinned
  adaptation and verified kernel receiving-interface filtering.
- Support Windows packet-info/scoped UDP replies without CGO.
- Add nonblocking advertisement error notifications and recovery health snapshots.
- Validate IPv4/IPv6 publication and cleanup with an independent isolated observer.

Requires Go 1.27.3+. This release does not claim complete RC1 conformance or
physical audio synchronization. The published v0.1.0 tags remain unchanged.

## v0.1.0

Initial release
