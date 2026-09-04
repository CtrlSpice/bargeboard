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
- Agents have standing authorization to commit completed, verified changes and
  open draft pull requests without requesting per-action approval.
- If the user explicitly authorizes push-on-commit for the active branch, treat
  that as standing authorization until the branch is merged or they revoke it.
- Use one-sentence commit messages that describe the completed change.
- Squash-merge pull requests so `main` receives one clean commit per feature.
- Do not rewrite published branch history or force-push unless explicitly approved.

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
