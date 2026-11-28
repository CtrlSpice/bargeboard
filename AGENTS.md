# Repository instructions

## Orientation

| Need | Where to look |
| --- | --- |
| Canonical behaviour contract (`GREEN`/`YELLOW`/`RED`) | [docs/architecture.md](docs/architecture.md) |
| Active decisions and work state | [docs/agent-notes.md](docs/agent-notes.md) |
| Quick start, release verification, and notices policy | [README.md](README.md) |
| Build, format, vet, validate, and test targets | `Makefile` |

## Implementation

- Keep deterministic transformations in pure functions and I/O, clocks, and shared state in the imperative shell, staying idiomatic Go without forced FP abstractions.
- Update `docs/architecture.md` in the same commit whenever source semantics, ownership, reduction, OTLP projection, timestamps, cardinality, or failure policy change. Stop if code, tests, and `docs/architecture.md` disagree; `docs/agent-notes.md` cannot override the architecture.

## Testing

- Ship focused tests in the same slice as every behaviour change, and include a regression test that fails without the fix for every bug fix. Shape deterministic seams instead of adding sleeps or broad integration tests, test pure functions directly, and assert the complete meaningful result.
- Source-dependent behaviour needs attributable evidence and compact, independently authored synthetic fixtures; keep observed source behaviour distinct from synthetic cases.
- Public CI must use invented credentials and local synthetic servers — never contact live F1 services or pass real credentials in CI.

## Release security

- Privileged release workflows run from protected `main` and treat signed tags as untrusted dispatch data.
- Release archives must include deterministic `THIRD_PARTY_NOTICES` and pinned corresponding source where required (see [README.md](README.md#releases)), and validate bounded canonical archives before extraction or SBOM scanning.
- The push whitespace gate must fail closed without mutating repo state, falling back to a full-tree check only on an explicit force-push without an old commit.

## Checks and workflow

- Before committing or opening a PR, run `make check`, `go test -race -count=1 ./receiver/f1livetimingreceiver` (for receiver changes), `bash scripts/check-push-diff_test.sh` (for whitespace-gate changes), and `git diff --check`.
- Develop on branches with small commits and one-sentence messages, squash-merge to `main`, and do not rewrite published history or force-push without approval.
