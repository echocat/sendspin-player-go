# RC1 requirements and execution evidence

## Where to look

| File | Purpose |
| --- | --- |
| `docs/rc1-requirements.json` | Source of truth: historical groups, independently classified atomic obligations, group-review decisions, test links and limitations |
| `docs/rc1-source-index.json` | Pinned source hashes/paragraphs plus frozen group and atomic-obligation IDs; existing IDs cannot silently disappear |
| `docs/rc1-audit.template.md` | Handwritten context around the generated requirement tables |
| [rc1-audit.md](rc1-audit.md) | Generated human-readable tables, including links to actual test files |
| `var/audit/report.md` | Generated report combining reviewed assessments with actual execution evidence |
| `var/audit/report.json` | The same report in machine-readable form |
| `var/audit/summary.md` | Compact CI summary; detailed notes remain in the full reports |

The baseline is specification **1.0.0-rc1** at
`671a34d408581fc25ea56b3528a4a3f13e3be901`, not the official harness's distinct
assertion revision. The checker matches the implementation's protocol profile.

**The normative inventory review is still partial.** Existing groups can combine
mandatory, recommended, optional and server-owned clauses. A `mixed` level must
not be read as one fully classified atomic requirement. README definitions and
scope/applicability outside these nine documents are not automatically complete.

The separately pinned `template.md` now has a complete **source-only** inventory:
`rc1-template-inventory.json` covers all 169 lines and 34 atomic predicates for
definitions, roles and protocol evolution. Its IDs N01-N34 are not extra historical
groups or inherited test passes. The checker rejects missing clauses, coverage
gaps, hidden normative sections, unknown primary mappings and fulfillment claims.
The generated audit/JSON report expose this supplement; external referenced texts
and the three unresolved primary group classifications remain separate.

All **148 historical groups** have received atomic review, including the remaining
core, discovery, artwork, visualizer and finer code-flow fields/states. **145 group
classifications are complete; three remain partial** (P08, CN15 and MD01).
[Explicit review decisions](rc1-review-decisions.md) classify the security property,
conditional withdrawal/printed-label duties and out-channel/QR guidance without
discharging their missing evidence. Numeric domains, timing derivation and
pre-authentication refusal remain unresolved, not invented optional exclusions.
No complete role implementation or normative inventory follows. Its
`obligations` do **not** replace or overwrite the historical `requirements` rows.
Each obligation has an exact level (including prohibition polarity), actor,
condition, applicability scope, normative basis, assessment, verification modality,
own source/implementation/test links and a remaining-work category/priority.
`normative_reviews` records which groups are classified completely or only partly.
Complete **classification** does not mean implemented, tested or fulfilled.

[Provisional working assumptions](rc1-working-assumptions.md) allow targeted work
to continue while interpretations remain open. The catalog's `working_assumptions`
pin C++ comparison locations, state the selected policy/differences and record when
to revisit it. Their test links and execution results are independent of atomic
obligations: passing a policy test never supplies normative evidence, changes a
scope/assessment or resolves hardware verification. They remain `provisional` in
both JSON and Markdown reports, and do not relax the strict conformance gate.

The report derives `prioritized_remaining_work` from these explicit dispositions:

- `implementation`: a reviewed missing implementation/recommended capability,
  not automatically a mandatory protocol defect;
- `software-evidence`: existing code needs a narrower targeted assertion;
- `measurement`: physical audio, backend or deployment calibration evidence;
- `review`: unresolved normative force or conditional applicability;
- `platform-network`: platform/network execution still needed when so classified.

Priority 1 covers mandatory evidence or release-critical normative decisions;
priority 2 covers recommendations/supporting evidence; priority 3 is lower priority.
Server-owned requirements and unselected optional policies stay visible as
`external`/`optional` dispositions but are not SDK bugs in the prioritized backlog.
The work list is still **partial**: unresolved classifications and external/README
definitions can introduce work. Groups lacking completed classification are listed
separately; none of the historical groups now lacks a review disposition.

The existing software-evidence backlog has been worked through with independently
selected assertions rather than relabelling it as measurements or optional scope.
Its tests include real handler closure, native pairing collisions/cancellation,
FileStore restarts, exact field inventories, codec/packet boundaries and elapsed
actor-timer execution. `T` still means finite targeted software evidence under
the documented callback/Store execution contract: not universal hard-real-time
preemption, physical measurement or complete normative classification. The three
partial group classifications and separately recorded measurement/platform work
remain visible. Run recovery now preserves final activity purpose so pairing-only
transport loss is not silently resumed.

The native inadmissible-admission fixture keeps its explicit invalid activation
across reference-connector retries. A one-shot fault otherwise turns a later
retry into an unrelated valid contender blocked by the test's intentionally held
admission lock. The regression exercises two connection generations and requires
all rejected sessions to leave before unlocking. Normal fault scenarios remain
one-shot; neither the SDK nor the strict reference implementation is relaxed.

An explicit `enabled_by` link can exclude conditional duties inside an unselected
client MAY branch (for example parallel pairing). The checker rejects an unrelated
or nonexistent permission and rejects unconditional client MUST/SHOULD duties
relabelled as optional. The conditional row must cite the enabling permission's own
source range, not merely share a broad historical group with an unrelated MAY.
Unknown normative strength/applicability stays open review and cannot occur in a
completely classified group. `basis` distinguishes explicit
keywords, declarative field/algorithm contracts and nonbinding examples rather
than blindly counting uppercase words.

Atomic test links and assessments are never inherited from a parent or another
atom. A passing unit/harness test cannot promote a partial hardware obligation;
the checker rejects `T` with hardware verification. Existing `T/C/P/O/S/D` parent
assessments remain unchanged. Shared clauses may link several groups but have one
canonical atomic ID. The first pass also corrected P18's processing-latency source
to include P:115, without changing its partial assessment.

The version-0 operator parser is classified as an `sdk-contract`: the SDK does
not have to implement a Sendspin server UI, but its provided helper must satisfy
the version-specific leniency, rejection and reserved-extension behavior it
promises. A fixed emitter vector (RFC 7748 Alice key plus a known PSK, independently
Base32-encoded) checks output layout/prefix/transliteration without relying only
on the lenient SDK parser. It is not a randomness-generation or cryptographic
primitive certification.

Unpaired-policy tests observe only a typed capability boolean and attempt inverse
remote fields through the real encrypted reference. They prove the immutable
local profile, not an unimplemented runtime withdrawal API. Code-method `D`
obligations remain conditional exclusions, not tested implementations. The broad
code-flow group is classified, but physical printed-token evidence is still
missing. Its pending/init/auth/confirm/retry grammar, each remote verification
check/order, wrapper-processing gate and failure cleanup are now decomposed;
this does not mean that any unoffered code method was implemented.

The paragraph index conservatively includes text next to headings. At migration,
75 complete source paragraphs were not fully referenced by the old grouped audit.
The first paragraph review assigned 58 to explicit requirement references and
recorded 17 as individually justified context/examples. There are currently no
unresolved paragraph dispositions **within those nine documents**, but normative
atomic classification, README/scope completeness and implementation/hardware
evidence remain partial. Zero open paragraph reviews is **not complete conformance**.
Reference footprint, audit-group counts, Go line coverage and conformance are
different quantities; the report does not invent a spec-coverage percentage.

`source_reviews` accepts only `context` or `example`, with an explanation and
related existing requirements in that document. It cannot set an implementation
assessment or test result. Optional/server-owned normative clauses remain ordinary
requirement rows with explicit applicability; they cannot disappear into a context
disposition. The report retains all paragraphs without full numeric references,
then separately shows which have a justified review and which are still open.

## Commands

```text
mise run lint:audit
mise run format:audit
mise run audit:verify-spec
mise run audit:report
mise run --jobs 1 check:full
mise run audit:check-conformance
```

- `lint:audit` is offline. It validates schema, unique IDs, source bounds, matching
  human/machine citations, existing implementation files, actual Go test
  declarations, correct suite membership, frozen group/obligation IDs, atomic
  parent/source consistency, conditional exclusions, explicit source gaps,
  source reviews with related groups, and generated-document freshness. Every SDK E2E/mDNS test must be linked or have
  an explicit reason why it is not claimed as normative evidence.
- `format:audit` regenerates the tables after code/module formatting. Edit JSON or
  the template, **not the generated audit**. `format` and `lint` include these tasks.
- `audit:verify-spec` explicitly fetches the nine pinned upstream documents and
  verifies the entire source and paragraph inventory. It also verifies separately
  pinned `interpretation_sources` (currently template.md's normative-language
  context) without claiming those documents have complete atomic review. It never updates a pin or
  copies upstream prose into the checked-in index. This network check is included
  in the full gate and the CI report job; ordinary unit tests remain offline.
- `audit:verify-noise` verifies the independent Cacophony source and frozen subset;
  ordinary vector tests are offline and do not generate their own expected bytes.
  See [oracle scope](noise-vectors.md). The existing PortAudio CI is unchanged.
- `audit:verify-mdns` reproduces/checks the private source-pinned mDNS adaptation
  against its upstream SHA-256 values and explicit scope/logging transformations.
  See [dynamic publication](dynamic-advertisement.md); ordinary tests stay offline.
- `audit:report` renders available evidence without pretending unrun suites passed.
  This default `catalog` profile is an inspection report, not the complete
  software-execution gate.
- `check:full` executes the existing software gates, records evidence, verifies
  source pins and finally requires the `full` evidence profile. Serial execution
  avoids contention between independent numeric-port-based fixtures.
- `audit:check-conformance` is a **stricter, currently blocked** gate. It requires
  current full execution evidence and completed normative review/mandatory
  evidence. It is intentionally not used to turn today's partial implementation
  into a green conformance certificate.

Native checks remain separate and require PortAudio development files:

```text
mise run lint:native
mise run test:native
go run ./internal/specauditcmd report --profile native --output var/audit/native
```

They are native build/callback tests, **not physical audio measurements**.

## What a recorded pass means

The existing mise test tasks now invoke the small Go evidence runner, which runs
the real commands rather than importing an old log or fabricating test results:

| Suite | Execution |
| --- | --- |
| `unit` | `go test -json -count=1 -race ./...` |
| `e2e` | Real reference SDK suite with e2e build tag and race detector |
| `mdns` | Existing isolated Linux multicast runner; its fresh output is converted by Go's test2json tool |
| `harness` | Existing pinned official-harness runner, with an exact fresh result sidecar |
| `reference` | Live pinned C++ time-filter comparison |
| `fuzz` | Every discovered fuzz target in a bounded actual fuzz campaign, not its ordinary seed-only run |
| `examples` | CGO-free example tests |
| `portaudio`, `native-example` | Separate native module/example tests with CGO |

Plain `go test` is still available and does not require mise. It does not create
traceability evidence by itself. Use the managed tasks for a collected report.

Go outcomes are read from test2json actions, never from printed words such as
“PASS”. A missing terminal/package event is incomplete. A failed package or
failed/skipped child cannot be hidden by a parent's pass. Missing and skipped
test references are not passes. A fuzz campaign can only be credited from the
fuzz profile, with its exact target/time arguments; unit seed passes do not count.
`-count=1` disables reuse of prior test results, not the shared compiler/module caches.
The initial catalog links top-level test declarations; this does not automatically
prove the semantic coverage of every table-driven subcase.

Harness results must have all 15 unique known scenarios, matching implementation,
spec/harness/assertion/reference pins, successful client/server exits and an
explicit non-unsupported outcome. A selected subset is useful for debugging, but
cannot satisfy the full harness suite. The upstream verdict implementation remains
unchanged, and the harness assertion revision is not relabelled as normative RC1.

## Provenance and freshness

Every suite records under `var/audit/evidence/<suite>/<context>/`:

- OS, architecture, Go version, optional execution variant and CI run/attempt;
- catalog SHA-256 and a fingerprint of implementation/tests, source fixtures,
  lockfiles, catalog/template and task/CI definitions;
- the executed arguments and explicit environment overrides (not secret-bearing
  copies of the entire environment);
- process completion/exit status and SHA-256 of the actual result logs.

Temporary compiler/test work and shared Go/lint/uv caches keep their normal system
locations. `var`, Git and IDE files are excluded from the source fingerprint.
Text line endings are normalized so equivalent Windows/Linux checkouts correlate.
Evidence subprocesses explicitly use `GOWORK=off` and `GOFLAGS=-mod=readonly` so
an outside workspace or ambient flags cannot silently change the pinned module
graph, tags or test selection. These are per-invocation settings, not global Go
configuration or cache/temp-directory changes. Plain Go commands remain unaffected.
Before execution, the runner queries the actual Go context in the command's
module directory. A different PATH/toolchain/target is rejected rather than
mislabelled with the version of a previously compiled runner. The mDNS scope is
still explicitly its independently pinned Linux container, not native host audio/networking.

An incomplete manifest is published **before** execution. The latest explicit
invocation replaces that context's current pointer even if it fails; a killed run
cannot fall back to a previous pass. Unique run subdirectories retain diagnostics.
Concurrent writes to the same slot fail instead of racing. After forced process
termination, inspect its incomplete manifest and confirm the process has stopped
before removing only that slot's `running.lock` and rerunning it.

The report rejects/flags changed source/catalogs, changed logs, command/profile
differences, missing outputs, ambiguous duplicate contexts and incomplete runs.
Source changes during execution invalidate that run. This is reproducibility and
traceability metadata, not cryptographic attestation against someone deliberately
rewriting the checker and all evidence. Review of code and assertions still matters.

## CI report

CI uploads evidence on **success and failure**. The final **RC1 traceability (not
certification)** job downloads the artifacts, verifies the catalog/source pins,
gates fresh complete Linux/amd64 Go 1.27.3 software and native-test profiles, adds
the summary to the GitHub Actions job summary, and uploads
**`rc1-traceability-report-attempt-N`**. The raw evidence remains in
`spec-evidence-*-attempt-N` artifacts. Evidence is scoped to the current CI
run/attempt; after a forced/failed attempt, rerun the complete workflow rather
than only the report job. This deliberately does not reuse another attempt's pass.

The report explicitly selects an OS/architecture/toolchain context. Stable-Go
matrix executions use a separate variant, even when stable happens to equal the
pinned Go version; results are never silently merged to select a passing attempt.
Other platform jobs retain their own execution evidence and workflow status.
Linux container multicast is not credited as native Windows/macOS discovery.

The software gate can be green while the report's **conformance gate remains
BLOCKED**. Passing evidence does not promote `C`/`P`/`O`, remove existing limitations
or turn an optional/server-owned clause into a fulfilled client requirement.
In particular, neither callback tests nor Oto's opt-in approximate output establish
the physical +/-1-ms timing, correction audibility or startup-quality requirements.

## Closing a requirement properly

1. Review the pinned source and split mixed groups when atomic classification is
   needed; add explicit obligations and group-review decisions without deleting
   baseline groups. Freeze new obligation IDs in the index and update its catalog
   digest after review; do not silently change/remove existing atom identities.
2. Implement the behavior and link a real targeted test in its actual suite.
3. Record what the assertions establish and what remains untested; static link
   existence is not proof that the assertions are sufficient.
4. Regenerate, lint, execute fresh evidence and inspect the report.
5. For source blocks newly referenced or newly identified, explicitly update the
   open-review/context dispositions. Do not label a normative behavior as context
   merely to remove it from the gap list. Do not erase baseline IDs or mark the normative
   inventory complete merely to satisfy a gate.
6. Perform the separate platform/hardware measurements before making the physical
   or complete conformance claim. No automated release/tag/publication is performed.
