# Repository Instructions

These instructions supplement `/Users/moya/Workspace/AGENTS.md` for work in this repository.

## Implementation

- The Go Collector distribution is the active implementation.
- Keep the TypeScript replay CLI as a reference until the Go implementation reaches feature parity. Do not remove it without explicit approval.
- Prefer a functional core and imperative shell: keep deterministic transformations free of I/O and shared state where practical, and test pure functions directly.
- Keep the result idiomatic Go; do not introduce abstractions solely to imitate functional programming.

## Development Workflow

- Develop features on branches rather than directly on `main`.
- Keep branch commits small, coherent, and easy to review. Include tests with the behavior they cover.
- For this repository, the standing authorization below overrides workspace-level
  requirements for per-action approval to commit, push feature branches, and
  merge pull requests.
- After behavior explicitly requested by the user or an architecture plan
  explicitly approved by the user is clear, agents may create a feature branch,
  commit completed verified changes, push the branch, open and update its pull
  request, mark it ready, squash-merge it after every landing gate passes, and
  fast-forward local `main`.
- This authorization ends after the pull request merges and one `--ff-only`
  synchronization of local `main`, or when the user revokes it. It does not
  authorize release tags, releases, deployments, branch deletion, force-pushes,
  history rewrites, ruleset bypasses, or new unapproved product or architecture
  decisions.
- Agents may use existing authenticated Git and GitHub tooling non-interactively
  for the authorized operations. They must not read, export, print, transmit, or
  modify credential material.
- Merging a pull request that itself triggers a release or deployment requires
  explicit user approval for that release or deployment.
- Use one-sentence commit messages that describe the completed change.
- Squash-merge pull requests so `main` receives one clean commit per feature.
- Do not rewrite published branch history or force-push unless explicitly approved.

## Test Development

- Every changed behavior must include focused tests in the same behavior slice
  and pull request. Passing the existing suite alone is not sufficient.
- Write tests before or alongside implementation when practical so they shape a
  deterministic seam rather than merely confirm finished code.
- Test pure transformations directly without I/O, clocks, logging, or shared
  mutable state.
- Cover accepted behavior, relevant boundaries, failure policy, state
  preservation, and prohibited behavior. Every bug fix requires a regression
  test that would fail without the fix.
- Source-dependent behavior requires compact attributable fixtures.
- Assertions must cover the complete meaningful result so future state or output
  fields cannot silently escape the test oracle.
- If behavior is difficult to test deterministically, improve the seam before
  merging rather than relying on sleeps or broad integration tests.
- Documentation-only architecture decisions must record the verification their
  eventual implementation will require.

## Independent Review Gate

- Before an agent merges any pull request, launch at least two fresh independent
  review agents. Reviewers must not have authored or edited the change, must not
  see each other's findings before all isolated reviews finish, and must not
  mutate files, the index, refs, or remotes.
- Give each reviewer the complete pull-request diff identified by exact base and
  head commit OIDs, user intent, applicable architecture, and required tests. One
  review must focus on correctness and architecture; another must adversarially
  examine edge cases, failure policy, tests, security, and regressions.
- Add a focused specialist review whenever a pull request changes timestamps,
  concurrency, protocols, credentials, cardinality, or another identified
  high-risk domain.
- Inspect and adjudicate every finding against code, tests, and canonical
  architecture. Do not accept or dismiss a finding mechanically.
- Every valid finding about behavior changed, exposed, or materially affected by
  the pull request blocks merge until fixed, including minor correctness,
  clarity, consistency, and maintainability nits. A valid unrelated pre-existing
  issue must be reported and tracked without silently expanding the pull request.
  Preference-only suggestions are not automatically valid and must not create
  unnecessary abstraction or scope.
- Any head or base OID change invalidates prior reviews. Rerun at least two fresh
  independent reviews, plus every required specialist review, against the final
  diff. Resolve contradictory findings from evidence, architecture, and tests;
  ask the user when a genuine product or architecture decision remains.
- Record the final reviewed base and head OIDs, review mandates and outcomes,
  finding resolutions, and exact verification commands in the pull request. All
  isolated reviews must finish before any finding is posted there. Review agents
  do not replace required local checks or CI.
- Before merge, require every configured required check for the exact final merge
  candidate to complete successfully. Superseded, skipped, neutral, and cancelled
  runs do not satisfy a required check.
- Inspect the complete diff against current `main`. If the base has moved, update
  the branch without rewriting published history, rerun affected local checks and
  all final reviews, and record the new OIDs. Confirm no unintended worktree
  change or unresolved valid finding remains.
- Correctness, security, and architectural consistency outrank schedule, patch
  size, and the desire to merge. Never self-approve or bypass a repository rule.

## Architecture Documentation

- `docs/architecture.md` is the canonical architecture for the active Go distribution. The TypeScript signal model in `README.md` is historical and non-authoritative.
- Any commit that changes source semantics, source ownership, state reduction, OTLP representation, timestamps, cardinality, or signal failure policy must update `docs/architecture.md` in the same commit.
- Clearly mark unresolved candidates as non-binding. Do not implement a pending signal mapping as if it were accepted.
- Keep architecture changes useful to both human and agent contributors: record rationale, rejected alternatives, source limitations, implementation seams, and required verification.

## Verification

- Run `make check` for Go changes.
- Run `go test -race -count=1 ./receiver/f1livetimingreceiver` for F1 Live Timing receiver changes.
- Run `npm run typecheck` while the TypeScript implementation remains in the repository.
- Run `git diff --check` before committing or opening a pull request.
