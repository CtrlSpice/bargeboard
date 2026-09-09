#!/usr/bin/env bash
set -euo pipefail

readonly tag="${1:?usage: validate-release-ref.sh TAG MAIN_COMMIT}"
readonly main_commit="${2:?usage: validate-release-ref.sh TAG MAIN_COMMIT}"
readonly required_check_app_id=15368
readonly semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'

if [[ ! "$tag" =~ $semver ]]; then
  printf 'release tag is not strict SemVer with a v prefix: %s\n' "$tag" >&2
  exit 1
fi
if [[ ! "$main_commit" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'current main is not a full commit OID: %s\n' "$main_commit" >&2
  exit 1
fi

payload="$(jq -ce .)" || {
  printf 'release-ref evidence is not valid JSON\n' >&2
  exit 1
}
readonly payload

if ! jq -e --arg tag "$tag" '
  .event.ref == ("refs/tags/" + $tag) and
  .event.created == true and
  .event.deleted == false and
  .event.forced == false and
  .event.after == .tag.sha and
  .ref.ref == ("refs/tags/" + $tag) and
  .ref.object.type == "tag" and
  .ref.object.sha == .tag.sha and
  .tag.tag == $tag and
  .tag.object.type == "commit" and
  .tag.verification.verified == true and
  .tag.verification.reason == "valid" and
  .commit.sha == .tag.object.sha and
  .commit.verification.verified == true and
  .commit.verification.reason == "valid"
' <<<"$payload" >/dev/null; then
  printf 'release ref is not one verified annotated tag over one verified commit\n' >&2
  exit 1
fi

tag_commit="$(jq -er '.tag.object.sha' <<<"$payload")"
readonly tag_commit
if [[ ! "$tag_commit" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'release tag target is not a full commit OID: %s\n' "$tag_commit" >&2
  exit 1
fi
if [[ "$tag_commit" != "$main_commit" ]]; then
  printf 'release tag %s points to %s, not current main %s\n' "$tag" "$tag_commit" "$main_commit" >&2
  exit 1
fi

if ! jq -e --argjson app_id "$required_check_app_id" --arg commit "$tag_commit" '
  ([.checks.check_runs[] | select(.name == "check" and .app.id == $app_id)]
    | sort_by(.started_at)
    | last) as $check |
  ([.workflow_runs.workflow_runs[] | select(
    .path == ".github/workflows/check.yml" and
    .event == "push" and
    .head_branch == "main" and
    .head_sha == $commit
  )]) as $runs |
  ($runs | length) == 1 and
  $check.status == "completed" and
  $check.conclusion == "success" and
  $runs[0].status == "completed" and
  $runs[0].conclusion == "success" and
  ($check.details_url | startswith(
    "https://github.com/CtrlSpice/bargeboard/actions/runs/" + ($runs[0].id | tostring) + "/job/"
  ))
' <<<"$payload" >/dev/null; then
  printf 'current main lacks a successful canonical check run from GitHub Actions app %s\n' "$required_check_app_id" >&2
  exit 1
fi
