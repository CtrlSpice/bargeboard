# Contributor Handoff

Read this file when starting or resuming work, including after context compaction.
It records decisions and work state; [architecture.md](architecture.md) is the
canonical behavior contract. Repository `AGENTS.md` governs collaboration and
landing. Notes, issue assignments, and milestones cannot override either source.

## Approved Decisions

### U-POLICY — Layered Unicode and input quality

**Approved policy; implementation pending.** The user approved the strategy and
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
| U-POLICY | Approved; documented | Canonical policy, this handoff, and the AGENTS resume pointer. Documentation validation checks approval fidelity, existing-contract consistency, links, and required future verification. |
| [U1 / #34](https://github.com/CtrlSpice/bargeboard/issues/34) | Approved; not implemented | Lossless quoted-string tokens and raw object keys; scoped control-string integration; valid Unicode and opaque payload preservation; direct boundary/duplicate/key tests. |
| [U2 / #35](https://github.com/CtrlSpice/bargeboard/issues/35) | Approved; not implemented | SessionInfo classification isolation and bounded issue propagation; synthetic malformed-name fixture; full independent-bundle/gate state and no-replay tests. |
| [U3 / #36](https://github.com/CtrlSpice/bargeboard/issues/36) | Approved; not implemented | Nonfatal plain/inflated payload-quality findings, bounded terminal cadence, receiver-only internal affected-update counts, and final summaries. Report actual input findings, not unimplemented projection outcomes. |

No Unicode implementation is present as of the implementation baseline below.
Do not infer implementation from a GREEN policy heading: its implementation status
is FORMATION LAP until verified code lands.

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
