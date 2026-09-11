# Repository Instructions

These instructions supplement `/Users/moya/Workspace/AGENTS.md` for work in this repository.

## Implementation

- The Go Collector distribution is the active implementation.
- Keep the TypeScript replay CLI as a reference until the Go implementation reaches feature parity. Do not remove it without explicit approval.
- Prefer a functional core and imperative shell: keep deterministic transformations free of I/O and shared state where practical, and test pure functions directly.
- Keep the result idiomatic Go; do not introduce abstractions solely to imitate functional programming.

## Release Implementation

- Privileged release workflows must execute from protected `main`; accept a
  signed release tag as untrusted dispatch data rather than executing workflow
  or script code selected by that tag.
- Native release archives must include deterministic third-party notices and
  pinned corresponding source for dependencies whose licenses require it.
- Validate an archive's bounded canonical representation before extracting it
  or passing it to an SBOM scanner.

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
- Remote-mutation authorization is limited to that behavior slice and, once they
  exist, its feature branch and pull request. It ends immediately when the pull
  request merges or closes without merging, when the work is abandoned or
  superseded, or when the user revokes it. After merge, only one immediate
  `--ff-only` synchronization of local `main` remains authorized; if it fails,
  stop and ask the user before doing anything else remotely.
- This authorization does not include release tags, releases, deployments,
  branch deletion, force-pushes, history rewrites, ruleset bypasses, or new
  unapproved product or architecture decisions.
- Agents may use existing authenticated Git and GitHub tooling non-interactively
  for the authorized operations. They must not read, export, print, transmit, or
  modify credential material.
- Any authorized operation that directly or indirectly triggers a release or
  deployment, including a push, pull-request state or metadata change, or merge,
  requires prior explicit user approval for that effect. Record the repository,
  operation, target ref or environment, and exact candidate OIDs when applicable;
  a change to any recorded value invalidates the approval.
- Any change to this standing authorization or its test and independent-review
  gates requires explicit user approval and must land under the version from the
  pull request's merge base. A proposed policy change cannot authorize or weaken
  the conditions of its own landing.
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
  eventual implementation will require. Normative policy changes must explain
  their non-executable validation when no focused automated seam exists.

## Independent Review Gate

- Before an agent merges any pull request, launch at least two fresh independent
  review agents. Reviewers must not have authored or edited the change, must not
  see each other's findings before all isolated reviews finish, and must not
  mutate files, the index, refs, or remotes.
- Give each reviewer the complete pull-request diff identified by exact base and
  head commit OIDs, user intent, applicable architecture, required tests, and the
  exact intended squash subject and body. One review must focus on correctness
  and architecture; another must adversarially examine edge cases, failure
  policy, tests, security, and regressions.
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
- Any repository, target branch, head or base OID, or intended squash subject or
  body change invalidates prior reviews. Rerun at least two fresh independent
  reviews, plus every required specialist review, against the final candidate.
  Resolve contradictory findings from evidence, architecture, and tests; ask the
  user when a genuine product or architecture decision remains.
- Final-round reviewers must not inspect pull-request discussion or any earlier
  review output. Record the repository, target branch, final reviewed base and
  head OIDs, review mandates and outcomes, finding resolutions, and exact
  verification commands in the pull request. All isolated reviews must finish
  before any finding is posted there. Review agents do not replace required local
  checks or CI.
- Before merge, require every configured required check for the exact final merge
  candidate to complete successfully. Superseded, skipped, neutral, and cancelled
  runs do not satisfy a required check. The canonical GitHub Actions `check` job
  must pass even if repository settings fail to require it.
- Inspect the complete diff against current `main`. If the base has moved, update
  the branch without rewriting published history, rerun affected local checks and
  all final reviews, and record the new OIDs. Confirm no unintended worktree
  change or unresolved valid finding remains.
- Keep the pull request draft until final checks and reviews pass. Before every
  push to a branch with an open pull request, before marking it ready, and again
  immediately before merging, confirm repository auto-merge is disabled and no
  merge queue applies. Stop if either condition is not met; never enable
  auto-merge, enter a merge queue, or use an administrative bypass.
- Immediately before merge, verify the repository, target branch, base OID, and
  head OID still match the reviewed candidate. Merge with the reviewed head OID
  and commit metadata, such as `gh pr merge <number> --repo <owner/repository>
  --squash --match-head-commit <reviewed-head-OID> --subject
  "<reviewed-one-sentence-subject>" --body ""`, while strict server-side
  protection requires the branch to remain current with `main`.
- GitHub's merge API atomically guards the head OID but has no corresponding
  expected-target or expected-base parameter. Strict branch protection supplies
  the base-freshness guard; the immediate target preflight assumes trusted
  collaborators will not retarget the pull request during the merge request. If
  that coordination assumption is not valid, require a user-performed merge.
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
