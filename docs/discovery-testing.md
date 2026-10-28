# Discovery and isolated multicast tests

## Runtime behavior

`Client.Serve` advertises `_sendspin._tcp.local.` with the persistent public
`ClientID` as its unique service instance. Friendly names are separate optional
TXT hints; two players called “Kitchen” do not collapse into one instance.
Overlong friendly names are omitted from TXT (255-byte DNS-string limit) but are
still sent in full in `client/hello`. The required `path` hint is validated.

Publication is restricted to addresses on the TCP listener's eligible interfaces.
An explicit IPv4 binding never advertises unrelated VPN/IPv6 addresses; wildcard
IPv6 listeners inspect `IPV6_V6ONLY` to distinguish IPv6-only from dual-stack.
Services have interface-specific hostnames/addresses instead of borrowing the
machine hostname and its unrelated records. IPv6 wildcard listener wrappers must
expose `SyscallConn`; otherwise disable automatic discovery and manage it explicitly.

Loopback-only listeners are not announced onto another interface. Set
`DisableDiscovery` for local/manual integrations. Once Serve accepts the inbound
mode reservation, it owns and closes its listener even if discovery startup fails.
It cancels/waits only its own HTTP/WebSocket handlers, not an unrelated server
mounting the same Client's `ServeHTTP` handler.

`discovery.Browse` uses the blocking client API from `libp2p/zeroconf/v2`. IPv4 and
IPv6 setup are independent, so an unavailable family does not discard working
discovery. Cancellation drains upstream subscriber sends and waits for socket
cleanup; it also cleans up while a caller callback panic unwinds. Callbacks must
still return promptly—arbitrary user code cannot be forcibly interrupted safely.

TXT keys are case-insensitive, and the first occurrence wins, including key-only
flags. Missing/invalid paths are ignored instead of guessing `/sendspin`. URI
escaping/query strings are preserved; invalid/broadcast addresses are discarded.
IPv4 link-local and global/ULA IPv6 endpoints are usable. IPv6 link-local results
now receive the **local receiving interface index** as their URL zone (`%25N` in
the serialized URL). Each resolver owns one interface/family, so the dependency's
missing per-entry scope cannot mix interface domains. TXT values cannot supply a
zone; results without a known scope remain ignored. Path/query escaping is kept.

Browse checks interface/address snapshots once per second. It cancels, drains and
joins the old resolver generation before using changed interfaces, waits through
temporary disappearance, and resumes on reappearance. Explicit interface lists
stay restricted to the same index/name identity; they never broaden to another
interface or a reused index. Callbacks stay serialized and cancellation is joined.
There must be an eligible interface at initial startup; snapshot/setup failures
are returned, not silently treated as discovered peers.

Advertisements now refresh per interface too, preserving the listener's actual
families, concrete IP and IPv6-zone restrictions rather than freezing the startup
NIC list. New wildcard-listener addresses are published, stale bindings withdrawn,
unchanged bindings retained and failed refreshes retried. No TCP listener is
rebound or session migrated. See [publication lifecycle](dynamic-advertisement.md).
Native Windows/macOS multicast hotplug, arbitrary reflectors/VLANs and deployment policy
remain separate validation work.

## Run the real multicast suite

```text
mise run test:mdns
```

This requires Podman (preferred) or Docker with a running **Linux** engine. On
Windows/macOS, start the runtime's Linux VM; no WSL-specific commands are needed.
To select an engine explicitly:

```text
go run ./internal/mdnstestcmd --engine podman
go run ./internal/mdnstestcmd --engine docker
```

The runner:

1. Creates a sanitized build context from Go sources and an explicit test-asset
   allowlist. It excludes Git/IDE data, virtual environments, credentials,
   arbitrary Python files, examples, generated logs and symlinks.
2. Builds a race-instrumented Linux test binary and the locked Python reference
   environment. Go, Python and uv base images are pinned by manifest digest.
3. Creates and inspects uniquely named **internal dual-stack** container networks.
4. Runs the fixture and an independent IPv6 observer in separate namespaces
   without host networking, published ports, bind mounts, privileged mode or
   daemon-socket mounts; container capabilities are dropped. The observer is
   attached only to the second internal network.
5. Attaches/removes its second internal network at a bounded, ordered test request,
   validating a newly discovered interface's IPv6 scope with an encrypted session,
   IPv4/IPv6 publication on existing listeners and socket cleanup. The container receives no daemon access or added
   capabilities; only the runner controls its own two network objects.
6. Removes its own container/networks and temporary context, including on normal
   interruption. Build/test logs remain under `var/mdns`; image layers stay
   cached. The artifact module boundary excludes those logs from SDK module zips.

Image building needs registry/module/package network access. Test execution uses
only the isolated internal network: it emits no test advertisements onto the
developer's LAN. Directly running the multicast-tagged tests on a host fails a
safety precondition; use the runner rather than bypassing that guard.

## Evidence and limits

The independent `python-zeroconf` peer announces a server or discovers the Go
player, then drives the real aiosendspin Noise session. Tests cover both connection
directions, authoritative hello names versus TXT hints, unique equally named
instances, address/port/path metadata, long optional names, goodbye withdrawal,
missing paths, cancellation, mode reservation and repeated UDP/5353 descriptor
cleanup. Ordinary unit tests separately cover listener ownership, interface/address
selection, TXT edge cases and upstream cancellation/panic cleanup.

This is real IPv4 and scoped link-local IPv6 multicast in a Linux network
namespace, with actual interface addition/removal, not a simulated DNS parser
test. It is also not proof of native Windows/macOS multicast behavior,
Wi-Fi multicast delivery or arbitrary LAN/firewall configurations. Native runtime
testing on those systems and broader deployment characterization remain necessary.
