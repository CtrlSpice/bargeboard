# Contributor Handoff

Read this file when starting or resuming work, including after context compaction.
It records decisions and work state; [architecture.md](architecture.md) is the
canonical behavior contract. Repository `AGENTS.md` governs collaboration and
landing. Notes, issue assignments, and milestones cannot override either source.

## Approved Decisions

### DEL-TS — Remove the obsolete replay implementation

**Landed in PR #45 at `2290ca4c45123d492a367d83d9de1df6b1c6855f`.**
The Go Collector distribution is the sole implementation, and the application
remains greenfield until explicitly declared complete. The deleted replay had no
users and known correctness defects, so its pre-completion CLI, cache, package,
configuration, resource, signal, and output formats have no compatibility or
migration contract.

The implementation deletes all 18 tracked files under `src/`,
`scripts/cache-season.ts`, `scripts/smoke.ts`, `package.json`,
`package-lock.json`, `tsconfig.json`, and `tsconfig.smoke.json`. It removes their
CI, Dependabot, ignore, README, architecture, and current-verification references.
No compatibility stub, deprecation wrapper, or cache migration is retained.

`scripts/publish-release.cjs` and `scripts/publish-release_test.cjs` remain as
Node-built-in-only privileged release tooling. The package job retains pinned
`actions/setup-node` with Node 24 and `node --test`; release-workflow CommonJS
loads remain unchanged. Repository-default CodeQL JavaScript analysis remains
enabled without a checked-in CodeQL workflow. The Go release archive shape,
third-party notices, source payloads, and SBOM policy are unchanged. Pre-existing
ignored `dist/` and `node_modules/` artifacts were removed from the working copy
after inspection; they were never tracked repository or release artifacts.

Local verification passed with pinned Go 1.26.8: `make check` and the full receiver
race suite. Both CJS files pass `node --check`; all 91 publication tests pass under
local Node 26.7.0. Pinned actionlint 1.7.12, GoReleaser Pro 2.18.1 configuration
validation, release-workflow tests, shell syntax, exact deletion/reference oracles,
and `git diff --check` pass. A clean five-platform release snapshot and complete
archive/SBOM/checksum verification also pass. Three independent final reviews and
exact-candidate CI, including JavaScript CodeQL for the retained CJS, passed.

### CAR-ORACLE — Complete attributable normalization evidence

**Landed in PR #47 at `462cdba8740241a25569ada34a5b5854ba5b3a06`.**
The pre-existing inline first 2025 British Grand Prix race `CarData.z` token is now
paired with pinned offline metadata: its direct source URL, archive prefix,
compressed-token SHA-256, expected 2,380-byte inflation length and SHA-256, and an
explicit synthetic feed-wrapper boundary. The pure normalization test compares the
complete topic, payload byte identity, timestamp, and source, preserves the complete
input, and proves output storage does not alias the compressed input. It does not
bind channel semantics or change production.

### FEED-ORDER-ORACLE — Complete SessionInfo feed-order results

**Approved test cleanup; implemented on `test/complete-feed-order-oracle`; pending
landing.** The strict wire-order test now compares every meaningful descriptor
reduction field for both A-then-B and B-then-A delivery, including complete
identity, routing, schedule, synchronization, generation, route epoch, retired
tuple order, disposition, route transition, and issues. Reversed source timestamps
continue to prove that feed callbacks are not globally time-sorted. Production and
the canonical feed-order contract are unchanged.

### U-POLICY — Layered Unicode and input quality

**Approved policy; U1/U2/U3 landed.** The user approved the strategy and
requested persistence in the repository. The exact contract and required tests
are in [Layered Unicode and Input Quality](architecture.md#layered-unicode-and-input-quality).

The decision to preserve after compaction is:

- Preserve independently useful data and raw payload representation during bounded
  processing. Do not silently replace malformed Unicode in identity or routing.
- Handle scalar-invalid strings at the smallest trustworthy semantic boundary:
  optional field, coupled bundle, entry, topic, or identity gate.
- A present invalid value is not an omitted sparse field. Do not let stale data
  remain projectable or refresh its age by dropping the bad member.
- SessionInfo meeting/type/name strings can classify the session. They are not
  universally cosmetic. Invalid identity suppresses dependent output while input
  continues; independent route/schedule failure retains its existing scope.
- Preserve snapshot manifests and commit semantic outcomes atomically. Do not
  invent missed events or retroactively attribute unbound input.
- Malformed scalar content in an otherwise valid payload must not automatically
  become a whole-batch protocol error or permanent input stop.
- Protocol control, byte encoding, JSON grammar, compression, and hard bounds keep
  their current rejection/recovery rules. Broader protocol recovery is unapproved.
- Make quality findings visible through bounded terminal/internal diagnostics and
  final summaries, without claiming racing data was dropped or recovered when
  that has not been established.

**Rejected:** blanket surrogate rejection followed by feed-wide permanent stop;
trusting Go's U+FFFD substitution for semantic strings; treating every name as
optional text; stripping invalid patch members; streaming snapshot siblings early;
automatic replay or new resubscription behavior hidden inside this fix.

## Current Implementation Queue

These stable IDs survive issue or milestone reorganization. Complete each verified
slice before landing the next; update this table in the same behavior PR when its
status changes.

GitHub milestone: [Scrutineering — layered input integrity](https://github.com/CtrlSpice/bargeboard/milestone/1).
It groups the implementation work; issue text links back to the canonical policy.

| ID | State | Scope and completion evidence |
|---|---|---|
| U-POLICY | Approved; landed in PR #37 | Canonical policy, this handoff, and the AGENTS resume pointer. Documentation validation checks approval fidelity, existing-contract consistency, links, and required future verification. |
| [U1 / #34](https://github.com/CtrlSpice/bargeboard/issues/34) | Landed in PR #38 | Lossless quoted-string tokens and source-order raw object-member visitor; scoped negotiation/capability/handshake/hub/feed/manifest controls; descriptive error metadata; opaque plain/inflated payload preservation. Focused scalar, duplicate/key/null/casing, setup prevention, A/B/C, snapshot atomicity, and runtime-stop regressions. |
| [U2 / #35](https://github.com/CtrlSpice/bargeboard/issues/35) | Landed in PR #39 at `0284a6265049b99bdf382ed1ed76aefb3d30cfec` | SessionInfo lossless classification and raw-key isolation; bounded issue propagation through descriptor/batch/gate results; attributable synthetic malformed-name regressions; complete independent-bundle/gate state, depth, atomicity, occurrence-union, and no-replay tests. |
| [U3 / #36](https://github.com/CtrlSpice/bargeboard/issues/36) | Landed in PR #40 at `7018b2a0e173b5619650498c71e6a7ef0636e0c9` | Nonfatal plain/inflated payload-quality findings after full batch validation; initial/coalesced warnings on the existing cadence; receiver-only affected-envelope counter and final summaries. Complete pure, byte/manifest/depth/limit, runtime-boundary, metrics-None, cardinality, and callback-summary oracles. |
| F-SCAN | Landed in PR #41 at `2ca79a5a91e46ca6bda2576ddd74c40dd5403b46` | Linear separator scanning across tiny fragments in runtime hub and handshake framing. Saved byte-relative cursors, deterministic scan-start and complete buffer-state oracles, bounds/ownership/failure checks, and a one-byte-fragment benchmark. |
| WS-ERROR | Upstream-first approved; local proposal prepared; dependency integration blocked | The pinned codec does not expose a typed identity for locally detected malformed frames. Prepare upstream classification support locally; public submission remains a separate decision. No dependency fork, replacement, or production classifier change is approved by this decision. |
| N-CONTROL | Landed in PR #43 at `6d2ce49b8fe1512699dd612db07c12256ee422ed` | Negotiation-only duplicate-known, case-alias, and present-null/entry rejection supersedes U1's negotiation compatibility policy. Fresh atomic capability replacement prevents inheritance. Attributed protocol examples and synthetic regression/matrix/setup oracles accompany the canonical update. |
| H-HOST | Landed in PR #44 at `3ba7b9713ec06dba261a8792af9715785becb50d` | Configured endpoints require a nonempty parsed hostname, with the existing bounded field error. Pure helper/configuration and all-signal factory regressions preserve full-authority/security/loopback rules and accepted nonempty-host syntax. |
| DEL-TS | Landed in PR #45 at `2290ca4c45123d492a367d83d9de1df6b1c6855f` | Deleted the obsolete replay implementation and package surface; retained only the Node-built-in release CJS boundary and future Go replay/OpenF1 architecture. No compatibility or cache migration. |
| CAR-ORACLE | Landed in PR #47 at `462cdba8740241a25569ada34a5b5854ba5b3a06` | Attribute the pre-existing first 2025 British GP race `CarData.z` token and assert the complete pure normalization result, input preservation, and detached output storage without binding YELLOW channel semantics. |
| FEED-ORDER-ORACLE | Approved test cleanup; implemented on `test/complete-feed-order-oracle`; pending landing | Assert complete SessionInfo descriptor reductions in both feed delivery orders while reversed timestamps prove strict callback order. |

U1 landed in PR #38 at `e2afacb0d6071f0a8e6a5c790c9039a3f52070d2`, the base of
the U2 implementation. U2 landed in PR #39 at `0284a62`, the U3 implementation base.
U3 landed in PR #40 at `7018b2a`, the F-SCAN implementation base. The
approved policy remains GREEN with partial FORMATION LAP implementation until the
future topic integrations are complete. The production normalized consumer remains
a no-op and SessionInfo helpers remain unwired.

### WS-ERROR Resume Details

- Raw-frame probes against `github.com/coder/websocket` v1.8.15 reproduced locally
  detected RSV/opcode/control/sequence/close-payload/negative-length violations
  returning ordinary errors, with `CloseStatus == -1`. The production receiver
  retries such errors. Received protocol-close errors and the read-limit sentinel
  remain distinguishable. Generic underlying I/O can have the same untyped error
  shape, so classifying all unknown errors as terminal would break network retries.
- At investigation time, v1.8.15 and upstream HEAD both identified
  `9c8faadccd1b679e811a79ce506f8a10237251ad`; no released upgrade exposed the missing
  identity. Observing outbound closes also misses negative lengths and failed
  close writes. No message-text classifier or new wire parser was implemented.
- The user chose upstream-first: prepare the patch and reproducer locally, then
  continue negotiation cleanup while integration waits. Public submission is a
  separate action. Bargeboard's dependency and runtime behavior remain unchanged.
- The local proposal adds `errors.Is(err, websocket.ErrProtocolViolation)` at
  existing native frame rejection sites. It preserves diagnostic text, wrapped
  causes, received `CloseError`, `ErrMessageTooBig`, and generic I/O behavior.
  It adds no validation rules. The sentinel is declared for js builds, whose
  browser API cannot expose these frame errors.
- This is an error-identity proposal, not a guarantee that every detected error
  reaches the caller: upstream cancellation/closure precedence remains unchanged.
  Independent review identified inflater buffering that invalidated an initial
  stronger claim; that claim and the proposed cleanup-precedence change were
  removed. Small-buffer shutdown/parity tests and a typed-cause wrapping oracle
  cover both resolved findings. Follow-up review found no remaining issues.
- Local materials are under `$TMPDIR/opencode/`: the complete six-file patch source
  is `websocket-protocol-violation/`; the report, public reproducer, isolated Go
  runner, and baseline overlays are in `websocket-protocol-violation-report/`.
  Include untracked `errors_notjs.go`, `errors_notjs_test.go`, and
  `protocol_error_test.go` when exporting the patch. These are local preparation
  artifacts, not an upstream commit, submission, acceptance, or release.
- Pinned Go 1.26.8 full upstream race tests, 20-repeat focused race tests, public
  reproducer, native Staticcheck, and supplementary vet passed. Baseline probes
  fail the new classification assertions while cancellation/closure parity passes.
  Native unqualified vet has the same ARM64 assembly declaration failure on base
  and candidate; Autobahn could not run without the Docker daemon. js/Linux
  compilation is not execution evidence. The local report records exact commands
  and verification scope; resolve remaining upstream checks before submission.
- The user approved applying the separate ARM64 correction locally. It is now
  implemented at `$TMPDIR/opencode/websocket-arm64-vet-fix`, based on
  `9c8faadccd1b679e811a79ce506f8a10237251ad`. Exactly two files change:
  `mask_arm64.s` corrects symbolic argument names `b_ptr+0`/`b_len+8` to
  `b+0`/`len+8`; `mask_asm_test.go` wires `TestMaskASM` to actual assembly and adds
  deterministic boundary/streaming oracles. Numeric ABI offsets and all 48 ARM64
  instruction words are unchanged. Production masking still calls Go.
- The report is `$TMPDIR/opencode/websocket-arm64-vet-fix-report/README.md`; its
  adjacent `arm64-vet-fix.patch` is the complete two-file patch, SHA256
  `96d74a4d5c902a29dbeac730766cd7a2ad3455e243c59ba9e011dc967f94ffea`.
  Full native vet/tests/race/Staticcheck and nested-module checks passed. AMD64
  direct-assembly tests and the root-module suite actually ran through Rosetta.
  A mutation proves the old Go-backed test missed an assembly defect and the new
  direct tests detect it. Independent read-only local patch review found no issues.
- Composing the original protocol proposal with the two-file assembly overlay
  passed full native vet and race tests. The original protocol checkout remains
  separate and unmodified by the assembly fix; raw unoverlaid vet still fails.
  Bargeboard's dependency remains unchanged. Public submission is not approved,
  and the prior Autobahn Docker-daemon blocker remains pending. The separate report
  records exact commands, platform execution limits, and combined-patch evidence;
  these local checks do not establish upstream acceptance or integration.

### N-CONTROL Resume Details

- Landed in PR #43 at `6d2ce49b8fe1512699dd612db07c12256ee422ed`, the H-HOST base.
- The user explicitly approved the stricter negotiation-only policy. The canonical
  [N-CONTROL contract](architecture.md#negotiation-control-acceptance-n-control)
  supersedes only U1's negotiation duplicate/case/null/slice-reuse compatibility.
  It rejects duplicate known controls (including identical/escaped equivalents),
  noncanonical case-fold aliases, present null known members, null capabilities,
  and null transfer formats. Unknown extensions stay opaque, including duplicate
  unknowns and malformed nested scalar content; inspected keys remain lossless.
- `visitNegotiateObject` keeps only bounded known-member seen state. Capabilities
  decode into fresh local values and commit only on success. Omitted-version v0,
  explicit v0/v1 identity selection, empty strings/incomplete objects, final
  token/error/redirect/capability decisions, and top-level-null missing-token
  classification retain their domain behavior. Handshake, hub, manifest, retry,
  global nullable helpers, and dependencies are outside this slice.
- ASP.NET Core v8.0.0 `TransportProtocols.md` examples and `NegotiateProtocol.cs`
  ground names/structure, not protocol-mandated duplicate or blanket null rejection.
  `testdata/negotiation/SOURCES.md` attributes the four compact examples and labels
  mutations as synthetic, with no claim of captured F1 wire evidence.
- Before implementation, both new rejection-erasure and array-inheritance tests
  failed on base `fc85d39a9a85ab2625bdbd139d1394bff6f85966`: `error:"denied"`
  followed by `ERROR:""` was accepted, and WebSockets/Binary plus a later
  `{transferFormats:["Text"]}` array element produced a usable capability.
  Existing U1 acceptance oracles now reflect the newly approved policy.
- Tests cover every known field's duplicate/case/null matrix (including irrelevant
  capabilities), exact escaped names and Unicode aliases, opaque extensions,
  domain decisions, complete decoded/selected results and zero-on-failure,
  unchanged input/destination/backing storage, depth/byte caps, and in-memory HTTP
  prevention of URL construction/upgrade with body closure and no returned
  cookies/partial connection.
- Local verification uses pinned Go 1.26.8 with `GOTOOLCHAIN=local`:
  `go test ./receiver/f1livetimingreceiver -run 'Test(NegotiationControls|ParseNegotiateResponse|UnicodeControls)' -count=1`
  and `go test -race -count=1 ./receiver/f1livetimingreceiver` passed. Full
  `make check` passed; `gofmt -d` for the changed Go files and `git diff --check`
  are clean. The exact-byte-cap success oracle distinguishes
  HTTP's empty cookie slice from nil cookies on failure. PR #43 records final
  reviews, CI, and landing evidence under `AGENTS.md`.

### H-HOST Resume Details

- Landed in PR #44 at `3ba7b9713ec06dba261a8792af9715785becb50d`.
- After the separate local upstream ARM64 fix, the user confirmed returning to
  endpoint hostname validation. The approved production change is confined to
  `validateEndpoint`: check `parsed.Hostname() == ""` after `url.Parse`, rather
  than `parsed.Host == ""`. The canonical contract is
  [Configured Endpoint Hostnames](architecture.md#configured-endpoint-hostnames-h-host).
- Tests first reproduced acceptance of matching secure `:443` and `:` pairs on
  base `6d2ce49b8fe1512699dd612db07c12256ee422ed`. All three signal factories
  returned a receiver and populated the cache. A malformed single field instead
  reached the authority-mismatch error. Pinned Go 1.26.8 already rejects `[]` and
  `[]:443` during parsing; these remain rejection-compatibility cases.
- Direct pure tests cover both configured field names and all four schemes, exact
  bounded errors, and complete configuration preservation. Accepted secure DNS,
  IPv4, bracketed IPv6, and insecure localhost/v4/v6 loopback pairs retain no-port
  and explicit-port forms. Underscores, empty ports, and numeric port 65536 remain
  accepted syntax, without a reachability claim. Full-authority comparison,
  security matching, and loopback restrictions retain focused compatibility tests.
- Traces, metrics, and logs factory tests assert nil receivers, exact field errors,
  unchanged configuration, and an empty cache. An unusable token-file reference
  needs no fixture or Start call; rejection precedes the lifecycle that owns file
  and network I/O. No new grammar, normalization, dependency, or retry behavior is
  part of H-HOST.
- Local verification passed with pinned Go 1.26.8 and `GOTOOLCHAIN=local`:
  `go test -count=1 ./receiver/f1livetimingreceiver -run 'Test(ValidateEndpointHostname|ConfigRejectsEmptyEndpointHostname|ConfigEndpointHostnameCompatibility|ConfigValidate|FactoriesRejectEmptyEndpointHostname)$'`
  and `go test -race -count=1 ./receiver/f1livetimingreceiver`. Full `make check`
  passed; `gofmt -d` for the changed Go files and `git diff --check` are clean.
  Independent reviews, CI, and landing evidence are recorded in PR #44.

### F-SCAN Resume Details

- The user approved reproducing incomplete-prefix rescanning, retaining scan
  progress for linear work, and covering both runtime hub and handshake framing.
  Implementation landed in PR #41 at `2ca79a5`, based on
  `7018b2a0e173b5619650498c71e6a7ef0636e0c9`.
- `splitFirstRecord` accepts the already-scanned prefix length. The hub buffer
  retains that cursor across incomplete checks, compaction, and append growth;
  consuming a record resets it for the next uninspected tail. Handshake reads keep
  the same offset locally and return only a successfully parsed response's tail.
  Whole-record bounds, accepted A before protocol-invalid B and no C, snapshot
  atomicity, failed-read byte discard, and recovery policy retain their contracts.
- The pre-fix one-byte-fragment benchmark with preallocated storage reproduced
  roughly 1.75/20.1/324 ms at 16/64/256 KiB, with zero allocations. After the fix,
  the same benchmark measured roughly 0.068/0.275/1.09 ms, also zero allocations.
  These are local supporting measurements, not timing-based acceptance gates.
- `TestFragmentScanStartsAtCursor` plants a separator in a previously scanned
  prefix solely as a white-box probe. Safely restoring the old byte-zero search
  made all three cases fail; independently forcing the hub buffer to pass offset
  zero also made all three fail. Both temporary mutations were restored. Complete
  state and storage-identity tests cover tiny/empty fragments, cursor resets,
  compaction, append growth, exact/plus-one bounds, and unchanged error state.
  Handshake and runtime tests cover coalesced tails, failure byte discard, and
  ordered delivery; the existing snapshot atomicity regressions also pass.
- Local verification passed with pinned Go 1.26.8 and `GOTOOLCHAIN=local`:
  `go test ./receiver/f1livetimingreceiver -run 'Test(FragmentScan|HubRecordBuffer|SplitHubRecord|SplitFirstRecord|HandshakeTiny|HandshakeFragment|Incremental)' -count=1`,
  `go test ./receiver/f1livetimingreceiver -run '^$' -bench '^BenchmarkHubRecordBufferTinyFragments$' -benchtime=100ms -count=1`,
  and `go test -race -count=1 ./receiver/f1livetimingreceiver`.
  The full `make check` and `git diff --check` also passed.
  Three independent reviews and exact-candidate CI passed; PR #41 records the
  final candidate and landing evidence.

### U3 Resume Details

- `hasInvalidJSONScalars` extracts and reuses U1's surrogate-pairing check. It scans
  validated original JSON without decoding keys/strings, allocating an AST, or
  adding a depth profile. Escaped backslashes, intentional U+FFFD, valid pairs, and
  literal astral text stay valid. Synthetic probes exercise keys, values, unknown
  content, duplicate occurrences, and plain/inflated payload boundaries.
- `normalizedLiveTimingBatch.invalidUnicodeUpdates` counts affected envelopes only
  after updates and manifests validate. A rejected batch returns zero findings;
  multiple bad strings in one envelope count once. Payload bytes, source ownership,
  and complete normalized manifests are preserved.
- `opBatch` commits the count before the consumer call. The operational reducer
  retains a total and reported-total watermark; first findings warn immediately,
  repeats flush only on the existing ticker's `opPeriodicTick`. Retry progress's
  separate `opTick` does not flush quality reports. The warning carries only total
  and newly reported affected-envelope counts, alongside any independent readiness
  notice. No status, outage, or semantic-recovery effect follows from a finding.
- `otelcol_f1livetiming_invalid_unicode_updates` is a synchronous Int64 counter,
  unit `{update}`, with only the configured receiver ID. The canonical instrument
  description and complete independent metric oracle are updated. Provider history,
  shared signal-factory ownership, multi-reader collection, and disabled-metrics
  terminal reporting retain the existing contracts.
- Controlled transport tests exercise the actual run with its production no-op
  consumer: a first affected snapshot at second 29, repeated findings across the
  exact 30/60-second boundaries, clean intervals, A/B/C continuation, and pending
  counts in the final summary. Separate tests cover rejected snapshots and shutdown
  timing out while a consumer callback holds completion. Summary totals describe
  input findings only, never projection, quarantine, dropped signals, or recovery.
- Local verification passed with pinned Go 1.26.8 and `GOTOOLCHAIN=local`:
  `go test ./receiver/f1livetimingreceiver -run 'Test(PayloadQuality|LosslessJSONString|UnicodeControls|Operational|HTTP)' -count=1`,
  `go test -race -count=1 ./receiver/f1livetimingreceiver -run 'Test(PayloadQuality|LosslessJSONString|UnicodeControls|Operational|HTTP)'`,
  and `go test -race -count=1 ./receiver/f1livetimingreceiver`. Formatting and
  `git diff --check` are clean. The initial full `make check` exposed an omitted
  root Collector integration metric oracle for `invalid_unicode_updates`. Its
  synthetic snapshots now exercise nonzero quality findings with Basic, None,
  and POSIX SIGINT, including receiver-only Prometheus labels, bounded warnings,
  complete summary totals, and unchanged status transitions. Focused root
  verification passed with the same pinned Go environment:
  `go test -count=1 -run '^TestForegroundCollectorOperationalTelemetry$' -v .`
  (Basic, None, and POSIX SIGINT). The full `make check` rerun and receiver race
  suite passed. CI and independent landing reviews remain
  governed by `AGENTS.md`; PR #40 records the completed landing and issue #36 tracks
  its evidence.

### U2 Resume Details

- `parseJSONString` uses U1's lossless token helper. Recognized scalar occurrences
  are decoded once before classification or typed grammar, including duplicates.
  `Meeting.Name`, `Type`, and `Name` belong to identity; `StartDate` also belongs
  to schedule. `EndDate` and `GmtOffset` remain schedule-only. Numeric keys retain
  raw integer grammar.
- Root/Meeting keys use the U1 whole-object visitor with the old end-to-end
  SessionInfo depth limit. Malformed scalar keys cannot name known ASCII members;
  they are skipped with a Unicode issue. Escaped known keys retain duplicate
  policy. Unknown value content stays opaque, including invalid scalars and
  duplicate metadata; U3 now detects payload-wide findings at normalization.
- The fixed `uint8` issue set gains a Unicode bit alongside existing domain bits.
  `sessionInfoReduction.issues` and `liveTimingReduction.sessionInfoIssues` carry
  parse occurrences through all outcomes without storing diagnostics in state.
  The batch contract still accepts exactly one feed update or an atomic snapshot
  with at most one SessionInfo. Callers combining feed results must OR issues
  separately from the last semantic state; later recovery does not erase findings.
- Complete-result tests cover exact recovery-only identity/route/schedule,
  generation, routing epoch, and retired FIFO preservation, independent bundle
  failures, idempotence, stale/exhausted outcomes, snapshot order and atomicity,
  source-byte ownership, and recovery without retaining intervening updates.
  Structural field-name assertions force review of new state/result fields.
- The initial attributed Abu Dhabi fixture mutation `"\uD800 Grand Prix"` failed
  before implementation because it classified as `practice_1` with no issues.
  The regression and all focused SessionInfo/parser/reducer/gate checks now pass.
  Local verification passed using repository-pinned Go 1.26.8 with
  `GOTOOLCHAIN=local`: `make check`,
  `go test -race -count=1 ./receiver/f1livetimingreceiver`, and
  `go test -race -count=20 ./receiver/f1livetimingreceiver -run 'Test(SessionInfoUnicode|ParseSessionInfo|ClassifySessionInfo|ParsePositiveCanonicalInt64|ReduceSessionInfo|ReduceLiveTimingBatch)'`.
  `git diff --check` also passed. Inspect #35 and its linked
  PR for final candidate, CI, review, and landing evidence.
- These are pure helper outcomes, not runtime projection or quarantine. They emit
  no counters or logs and do not establish any other topic's resynchronization.

### U1 Resume Details

- `json_tokens.go` provides a pure single-decode scalar validator and a raw-member
  visitor. The latter exposes original key/value views without a universal
  duplicate policy, nested string decoding, or a full custom JSON parser.
- U1's depth compatibility fix keeps two explicit profiles: whole-object
  validation for negotiation/handshake and per-member-value validation for
  hub/snapshot objects. The latter restores the pre-U1 outer-Token/per-value-Decode
  10,000-depth budget. A complete grammar-validation pass precedes callbacks;
  bounded raw token location handles malformed/truncated input without an AST.
  Regression tests reproduce the rejected boundary at U1 head `edf973f`, check
  accepted and limit-plus-one depths against the pre-U1 decoding pattern, and
  verify A/deep-valid-B/C continuation, snapshot atomicity, and shallow grammar
  equivalence with raw-view/input preservation.
- U1 originally preserved negotiation case-insensitive assignment, duplicates,
  null no-ops, and capability slice reuse; approved N-CONTROL supersedes only that
  compatibility policy. Handshake preserves case-sensitive keys and last
  raw error value. Hub and manifest policies remain strict. Invalid control keys
  cannot become unknown keys or collide through U+FFFD repair.
- Error descriptions use presence/string-shape/empty metadata only. Control
  failures retain setup rejection and runtime stop; scalar-invalid opaque payload
  bytes remain deliverable, including inflated JSON and all snapshot siblings.
- Current Grid semantics remain input-only: no-op normalized consumer, unwired
  SessionInfo helpers, no racing projection. U1 adds no quality counters or
  claims about semantic quarantine, dropped signals, or recovery.
- Local verification passed with repository-pinned Go 1.26.8 and
  `GOTOOLCHAIN=local`: `make check`,
  `go test -race -count=1 ./receiver/f1livetimingreceiver`, and
  `go test -race -count=20 ./receiver/f1livetimingreceiver -run 'Test(LosslessJSONString|RawJSONObject|NullableControlString|UnicodeControls)'`.
  `git diff --check` also passed. The initial focused
  regressions failed on repaired controls before implementation. Inspect #34 and
  its linked PR for final candidate, CI, review, and landing evidence.
- The same local checks passed after the depth compatibility fix, including the
  new boundary/grammar regressions in the 20-repeat race run. A bounded fuzz run
  also passed: `go test ./receiver/f1livetimingreceiver -run '^$' -fuzz '^FuzzRawJSONObjectMembersGrammar$' -fuzztime=15s -parallel=2`
  (72 synthetic seeds; 253,814 executions). No source acquisition is needed to
  reproduce these tests.

## Landed Cleanup Baseline

Implementation baseline: `main` through PR #29, commit
`bcb975c501159b234ec27ed8b91f0b1530c537d3`. This is a provenance marker, not a claim
about the current checkout; inspect Git status and current refs before acting.

| PR | Landed behavior |
|---|---|
| #20 | Reproducible, verified native release pipeline. |
| #21 | Publication evidence bound to the requested release identity. |
| #22 | Complete checksum EOF consumption and NUL rejection. |
| #23 | Identified source-attribution omissions fixed; LINPACK evidence resolved; scoped notices and supplements pinned. |
| #24 | Sanitized setup I/O and header-only preflight handling. |
| #25 | Incremental hub records and tested buffer compaction/reuse; approved A/protocol-invalid-B/C commitment. |
| #26 | Exact snapshot wire membership before `.z` normalization. |
| #27 | 15-second outbound keepalive and independent 30-second receive/subscription server-wait budgets, excluding local processing. |
| #28 | Terminal outage/progress/recovery/stop summaries and Collector internal metrics; ordered status delivery and context-bounded cleanup. |
| #29 | Stage-specific HTTP retry/terminal policy, monotonic Retry-After floors, and classification before redirect parsing. |

These PRs passed their required checks and independent landing reviews. The active
Go receiver still does not export racing signals: the normalized consumer is a
no-op, identity helpers are unwired, and multi-topic coordination/projection remain
future work. Internal input activity is not a completed race recording.

## Remaining Cleanup and Decisions

The broader adversarial cleanup sequence is approved: release integrity, active Go
transport, then test/seam/documentation cleanup. Research findings still need
adjudication and focused verification in their slices.

- Go transport: F-SCAN, N-CONTROL, and H-HOST landed; WS-ERROR awaits upstream
  classification support. The separate upstream ARM64 correction is implemented
  and reviewed locally; public submission remains unapproved.
- Pure-test oracles: CAR-ORACLE landed; FEED-ORDER-ORACLE is implemented pending
  landing. Counter and retirement-boundary candidates remain research findings
  that require focused adjudication. Keep the existing value-state functional core
  and idiomatic Go.
- Verification engineering: supplied-evidence SPDX tests, structural workflow-gate
  tests, and demonstrated redundant work/dead scaffolding. Preserve independent
  generator/verifier cross-checks and all landing gates.
- Toolchain verification: [#46](https://github.com/CtrlSpice/bargeboard/issues/46)
  tracks that Go 1.27 rejects the accepted JSON-depth boundary while the README
  currently claims Go 1.26 or newer. Pinned Go 1.26.8 and releases remain green;
  no depth-contract or supported-toolchain decision has been made.
- Fixture licensing: [#48](https://github.com/CtrlSpice/bargeboard/issues/48)
  tracks the owner/legal decision for pre-existing exact F1 archive bytes. This
  slice does not add the readable inflated CarData record; its expected length
  and SHA-256 retain the complete byte oracle without expanding that question.
- Decisions still pending: broader protocol resubscription after corruption,
  durable raw capture, topic-specific Unicode integration for unimplemented
  reducers, and qualifying-phase fallback ownership. The layered Unicode approval
  does not decide these.

## Resume Procedure

1. Read repository instructions, this handoff, and the relevant canonical policy.
2. Inspect the worktree, branch/base/head, open PRs, and issue/milestone state.
   Treat unfamiliar changes as concurrent work; never overwrite them.
3. Select the next approved slice from the queue. Keep proposed or unimplemented
   work distinct from landed behavior.
4. Include focused tests and the required architecture update. Run the applicable
   `make check`, receiver race, diff, and CI checks. Final reviews must
   identify the exact candidate and commit metadata under `AGENTS.md`.
5. Record the completed slice and any new decision before handing off or moving
   to the next slice. GitHub tracking should link to the canonical contract rather
   than copying a competing policy.
