#!/usr/bin/env bash
set -euo pipefail

readonly expected_repository=CtrlSpice/bargeboard
readonly repository="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
readonly tag="${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
readonly expected_ref="refs/tags/$tag"
readonly event_path="${GITHUB_EVENT_PATH:?GITHUB_EVENT_PATH is required}"

if [[ "$repository" != "$expected_repository" ]]; then
  printf 'release workflow is running in %s, not %s\n' "$repository" "$expected_repository" >&2
  exit 1
fi
if [[ "${GITHUB_EVENT_NAME:-}" != push || "${GITHUB_REF_TYPE:-}" != tag || "${GITHUB_REF:-}" != "$expected_ref" ]]; then
  printf 'release workflow requires a tag push for %s\n' "$expected_ref" >&2
  exit 1
fi

ref_json="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/git/ref/tags/$tag")"
readonly ref_json
tag_oid="$(jq -er '.object | select(.type == "tag") | .sha' <<<"$ref_json")"
readonly tag_oid
tag_json="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/git/tags/$tag_oid")"
readonly tag_json
tag_commit="$(jq -er '.object | select(.type == "commit") | .sha' <<<"$tag_json")"
readonly tag_commit
commit_json="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/git/commits/$tag_commit")"
readonly commit_json
checks_json="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/commits/$tag_commit/check-runs?filter=latest&per_page=100")"
readonly checks_json
workflow_runs_json="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/actions/workflows/check.yml/runs?branch=main&event=push&per_page=100")"
readonly workflow_runs_json
event_json="$(jq -ce . "$event_path")"
readonly event_json

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
if [[ "$checkout_commit" != "$tag_commit" || "${GITHUB_SHA:-}" != "$tag_commit" ]]; then
  printf 'workflow checkout is not the release tag commit %s\n' "$tag_commit" >&2
  exit 1
fi

payload="$(jq -cn \
  --argjson event "$event_json" \
  --argjson ref "$ref_json" \
  --argjson tag "$tag_json" \
  --argjson commit "$commit_json" \
  --argjson checks "$checks_json" \
  --argjson workflow_runs "$workflow_runs_json" \
  '{event: $event, ref: $ref, tag: $tag, commit: $commit, checks: $checks, workflow_runs: $workflow_runs}')"
readonly payload
if ! printf '%s\n' "$payload" | bash scripts/validate-release-ref.sh "$tag" "$main_commit"; then
  exit 1
fi

printf 'tag_oid=%s\ncommit=%s\n' "$tag_oid" "$tag_commit"
