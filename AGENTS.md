# Repository Instructions

These instructions supplement `/Users/moya/Workspace/AGENTS.md` for work in this repository.

## Implementation

- The Go Collector distribution is the active implementation.
- Keep the TypeScript replay CLI as a reference until the Go implementation reaches feature parity. Do not remove it without explicit approval.

## Development Workflow

- Develop features on branches rather than directly on `main`.
- Keep branch commits small, coherent, and easy to review. Include tests with the behavior they cover.
- Use one-sentence commit messages that describe the completed change.
- Squash-merge pull requests so `main` receives one clean commit per feature.
- Do not rewrite published branch history or force-push unless explicitly approved.

## Verification

- Run `make check` for Go changes.
- Run `npm run typecheck` while the TypeScript implementation remains in the repository.
