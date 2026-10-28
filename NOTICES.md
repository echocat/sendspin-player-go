# Third-party notices

Most of this repository is licensed under the MIT license in `LICENSE`.

`internal/mdns/{client,server,connection,service,utils}.go` is a generated,
locally adapted copy of libp2p/zeroconf v2.2.0 at
`586e7c571d106f842c8567012dacd28d73fcf966`. Copyright 2016 Stefan Smarzly and
2014 Oleksandr Lobunets; its MIT license is retained in `internal/mdns/LICENSE`.
Changes: verified receiving-interface filtering and required packet metadata,
SDK package/import namespace, and redacted slf4g transport diagnostics. The
source fingerprints and exact transformations live in `internal/mdnsimportcmd`.

`internal/timesync/filter.go` is a Go adaptation of the default-parameter algorithm
in Sendspin/time-filter, revision `531e2707a6e82f30efe335b0f87eaa4b29390021`:

- Copyright 2025 Sendspin Contributors.
- Licensed under Apache License 2.0; a copy is in `LICENSES/Apache-2.0.txt`.
- Changes: Go implementation, mutex/state representation, float-valued error
  reporting, an SDK readiness predicate and NTP sample construction. The original
  C++ default Kalman update and time-conversion equations are retained.

`internal/timesync/testdata/reference/sendspin_time_filter.cpp` and `.h` are
unchanged copies from that same upstream revision, retained solely as test oracles
under Apache License 2.0. Their original copyright/license notices are preserved.
The accompanying driver and Go comparison harness are our MIT-licensed test code.
Generated vectors record source/driver hashes and the native compiler fingerprint;
the reference sources are neither compiled nor linked into SDK binaries.

Go dependencies are not vendored; consult their respective licenses and pinned
module versions. The optional Oto output uses unmodified `oto/v3` (Apache-2.0),
`purego` (Apache-2.0), and `jfreymuth/pulse` (MIT); consult those modules for their
complete notices and transitive terms. PortAudio is a separate optional module.
`gopus` supplies Opus decoding; `aiosendspin` is a test-only
reference implementation, not copied into the SDK. The Sendspin specification is
referenced, not redistributed as part of this implementation.

`internal/wire/testdata/cacophony.json` extracts two public Noise test-vector
records from `centromere/cacophony`, commit
`8ee9d41e34a1a596cfa3ab12aa4069ff87dc1247`, under the Unlicense/public-domain
dedication. Its license is retained at `internal/wire/testdata/UNLICENSE.txt`.
The vectors contain published deterministic test keys, never device credentials.
The source fingerprints and both extracted transcripts are verified separately;
these data are test oracles, not generated from the Go implementation.
