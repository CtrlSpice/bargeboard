#!/usr/bin/env bash
set -euo pipefail

readonly expected_repository=CtrlSpice/bargeboard
readonly repository="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
readonly tag="${RELEASE_TAG:?RELEASE_TAG is required}"
readonly expected_ref="refs/tags/$tag"
readonly event_path="${GITHUB_EVENT_PATH:?GITHUB_EVENT_PATH is required}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir

if [[ "$repository" != "$expected_repository" ]]; then
  printf 'release workflow is running in %s, not %s\n' "$repository" "$expected_repository" >&2
  exit 1
fi
if [[ "${GITHUB_EVENT_NAME:-}" != repository_dispatch ||
  "${GITHUB_REF_TYPE:-}" != branch ||
  "${GITHUB_REF_NAME:-}" != main ||
  "${GITHUB_REF:-}" != refs/heads/main ]]; then
  printf 'release workflow requires a repository dispatch on main\n' >&2
  exit 1
fi
bash "$script_dir/validate-release-tag.sh" "$tag"

evidence_dir="$(mktemp -d)"
readonly evidence_dir
trap 'rm -rf "$evidence_dir"' EXIT

gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/git/ref/tags/$tag" >"$evidence_dir/ref.json"
tag_oid="$(jq -er '.object | select(.type == "tag") | .sha' "$evidence_dir/ref.json")"
readonly tag_oid
gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/git/tags/$tag_oid" >"$evidence_dir/tag.json"
tag_commit="$(jq -er '.object | select(.type == "commit") | .sha' "$evidence_dir/tag.json")"
readonly tag_commit
gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/git/commits/$tag_commit" >"$evidence_dir/commit.json"
gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/commits/$tag_commit/check-runs?filter=latest&per_page=100" \
  >"$evidence_dir/checks.json"
gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/actions/workflows/check.yml/runs?branch=main&event=push&head_sha=$tag_commit&per_page=2" \
  >"$evidence_dir/workflow-runs.json"
jq -ce . "$event_path" >"$evidence_dir/event.json"

git fetch --force --no-tags origin "$expected_ref:refs/release-validation/$tag"
readonly local_tag_ref="refs/release-validation/$tag"
local_tag_oid="$(git rev-parse --verify "$local_tag_ref^{tag}")"
readonly local_tag_oid
local_tag_commit="$(git rev-parse --verify "$local_tag_ref^{commit}")"
readonly local_tag_commit
git fetch --force --no-tags origin refs/heads/main:refs/remotes/origin/main
main_commit="$(git rev-parse --verify 'refs/remotes/origin/main^{commit}')"
readonly main_commit
checkout_commit="$(git rev-parse --verify 'HEAD^{commit}')"
readonly checkout_commit

if [[ "$local_tag_oid" != "$tag_oid" || "$local_tag_commit" != "$tag_commit" ]]; then
  printf 'GitHub API and Git disagree about release tag %s\n' "$tag" >&2
  exit 1
fi
if [[ "$checkout_commit" != "$main_commit" ||
  "${GITHUB_SHA:-}" != "$main_commit" ||
  "$tag_commit" != "$main_commit" ]]; then
  printf 'workflow checkout, release tag, and current main do not identify one commit\n' >&2
  exit 1
fi

if ! jq -cn \
  --slurpfile event "$evidence_dir/event.json" \
  --slurpfile ref "$evidence_dir/ref.json" \
  --slurpfile tag "$evidence_dir/tag.json" \
  --slurpfile commit "$evidence_dir/commit.json" \
  --slurpfile checks "$evidence_dir/checks.json" \
  --slurpfile workflow_runs "$evidence_dir/workflow-runs.json" '
    {
      event: $event[0],
      ref: $ref[0],
      tag: $tag[0],
      commit: $commit[0],
      checks: $checks[0],
      workflow_runs: $workflow_runs[0]
    }
  ' | bash "$script_dir/validate-release-ref.sh" "$tag" "$main_commit"; then
  exit 1
fi

printf 'tag=%s\ntag_oid=%s\ncommit=%s\n' "$tag" "$tag_oid" "$tag_commit"
