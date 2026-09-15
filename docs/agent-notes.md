# Contributor Handoff

Read this file when starting or resuming work, including after context compaction.
It records decisions and work state; [architecture.md](architecture.md) is the
canonical behavior contract. Repository `AGENTS.md` governs collaboration and
landing. Notes, issue assignments, and milestones cannot override either source.

## Approved Decisions

### U-POLICY — Layered Unicode and input quality

**Approved policy; U1 landed, U2 implemented, U3 pending.** The user approved the strategy and
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
| [U2 / #35](https://github.com/CtrlSpice/bargeboard/issues/35) | Implemented; landing tracked in #35 | SessionInfo lossless classification and raw-key isolation; bounded issue propagation through descriptor/batch/gate results; attributable synthetic malformed-name regressions; complete independent-bundle/gate state, depth, atomicity, occurrence-union, and no-replay tests. |
| [U3 / #36](https://github.com/CtrlSpice/bargeboard/issues/36) | Approved; not implemented | Nonfatal plain/inflated payload-quality findings, bounded terminal cadence, receiver-only internal affected-update counts, and final summaries. Report actual input findings, not unimplemented projection outcomes. |

U1 landed in PR #38 at `e2afacb0d6071f0a8e6a5c790c9039a3f52070d2`, the base of
the U2 implementation. Issue #35 tracks U2's implementation and landing. The approved
policy remains GREEN with partial FORMATION LAP implementation until U3 and the
future topic integrations are complete. U3 owns nonfatal payload-quality findings
and runtime diagnostics/counters; the production normalized consumer remains a no-op.

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
  duplicate metadata; payload-wide detection remains U3 work.
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
  `npm run typecheck` and `git diff --check` also passed. Inspect #35 and its linked
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
- Negotiation preserves case-insensitive assignment, duplicates, null no-ops,
  and capability slice reuse; handshake preserves case-sensitive keys and last
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
  `npm run typecheck` and `git diff --check` also passed. The initial focused
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
transport, retained TypeScript correctness, then test/seam/documentation cleanup.
Research findings still need adjudication and focused verification in their slices.

- Go transport: avoid rescanning incomplete prefixes; distinguish codec-detected
  malformed WebSocket framing from network failures; tighten negotiation duplicate,
  casing, and null handling; require a nonempty endpoint hostname.
- Pure-test oracles: complete attributable CarData and remaining reducer/gate
  assertions. Keep the existing value-state functional core and idiomatic Go.
- TypeScript reference: lap/sector ordering and completion, replay termination,
  cleanup/export failure handling, CLI/cache controls, cache completeness/atomicity,
  acquisition pagination/retries, histogram correctness, and source ownership.
  Retain the reference; its model is not authority for Go.
- Verification engineering: supplied-evidence SPDX tests, structural workflow-gate
  tests, and demonstrated redundant work/dead scaffolding. Preserve independent
  generator/verifier cross-checks and all landing gates.
- Decisions still pending: broader protocol resubscription after corruption, durable
  raw capture, topic-specific Unicode integration for unimplemented reducers,
  qualifying-phase fallback ownership, and unresolved TypeScript source/identity/
  delivery policies. The layered Unicode approval does not decide these.

## Resume Procedure

1. Read repository instructions, this handoff, and the relevant canonical policy.
2. Inspect the worktree, branch/base/head, open PRs, and issue/milestone state.
   Treat unfamiliar changes as concurrent work; never overwrite them.
3. Select the next approved slice from the queue. Keep proposed or unimplemented
   work distinct from landed behavior.
4. Include focused tests and the required architecture update. Run the applicable
   `make check`, receiver race, TypeScript, diff, and CI checks. Final reviews must
   identify the exact candidate and commit metadata under `AGENTS.md`.
5. Record the completed slice and any new decision before handing off or moving
   to the next slice. GitHub tracking should link to the canonical contract rather
   than copying a competing policy.
