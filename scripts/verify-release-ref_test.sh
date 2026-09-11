#!/usr/bin/env bash
set -euo pipefail

work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"

readonly commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
readonly tag=v1.2.3
readonly tag_oid=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
readonly workflow_endpoint="repos/CtrlSpice/bargeboard/actions/workflows/check.yml/runs?branch=main&event=push&head_sha=$commit&per_page=2"

cat >"$work/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
endpoint="${!#}"
case "$endpoint" in
  repos/CtrlSpice/bargeboard/git/ref/tags/v1.2.3)
    jq -cn --arg tag "$TEST_TAG" --arg tag_oid "$TEST_TAG_OID" '{
      ref: ("refs/tags/" + $tag),
      object: {type: "tag", sha: $tag_oid}
    }'
    ;;
  repos/CtrlSpice/bargeboard/git/tags/*)
    jq -cn --arg tag "$TEST_TAG" --arg tag_oid "$TEST_TAG_OID" --arg commit "$TEST_COMMIT" '{
      sha: $tag_oid,
      tag: $tag,
      object: {type: "commit", sha: $commit},
      verification: {verified: true, reason: "valid"}
    }'
    ;;
  repos/CtrlSpice/bargeboard/git/commits/*)
    jq -cn --arg commit "$TEST_COMMIT" '{
      sha: $commit,
      verification: {verified: true, reason: "valid"}
    }'
    ;;
  repos/CtrlSpice/bargeboard/commits/*/check-runs*)
    jq -cn '{check_runs: [{
      name: "check",
      app: {id: 15368},
      status: "completed",
      conclusion: "success",
      details_url: "https://github.com/CtrlSpice/bargeboard/actions/runs/42/job/100",
      started_at: "2026-09-08T00:00:00Z"
    }]}'
    ;;
  repos/CtrlSpice/bargeboard/actions/workflows/check.yml/runs*)
    printf '%s\n' "$endpoint" >"$CAPTURE_WORKFLOW_ENDPOINT"
    jq -cn --arg commit "$TEST_COMMIT" '{
      total_count: 1,
      padding: ("x" * 140000),
      workflow_runs: [{
        id: 42,
        path: ".github/workflows/check.yml",
        event: "push",
        head_branch: "main",
        head_sha: $commit,
        status: "completed",
        conclusion: "success"
      }]
    }'
    ;;
  *)
    printf 'unexpected gh endpoint: %s\n' "$endpoint" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$work/bin/gh"

cat >"$work/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  fetch)
    ;;
  rev-parse)
    case "${3:-}" in
      *'^{tag}') printf '%s\n' "$TEST_TAG_OID" ;;
      *'^{commit}') printf '%s\n' "$TEST_COMMIT" ;;
      *) printf 'unexpected git revision: %s\n' "${3:-}" >&2; exit 1 ;;
    esac
    ;;
  *)
    printf 'unexpected git command: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$work/bin/git"

jq -cn --arg tag "$tag" '{
  action: "release",
  client_payload: {tag: $tag}
}' >"$work/event.json"

output="$(
  env \
    CAPTURE_WORKFLOW_ENDPOINT="$work/workflow-endpoint" \
    GITHUB_EVENT_NAME=repository_dispatch \
    GITHUB_EVENT_PATH="$work/event.json" \
    GITHUB_REF=refs/heads/main \
    GITHUB_REF_NAME=main \
    GITHUB_REF_TYPE=branch \
    GITHUB_REPOSITORY=CtrlSpice/bargeboard \
    GITHUB_SHA="$commit" \
    PATH="$work/bin:$PATH" \
    RELEASE_TAG="$tag" \
    TEST_COMMIT="$commit" \
    TEST_TAG="$tag" \
    TEST_TAG_OID="$tag_oid" \
    bash scripts/verify-release-ref.sh
)"

if [[ "$output" != $'tag=v1.2.3\ntag_oid=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\ncommit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' ]]; then
  printf 'unexpected release-ref output:\n%s\n' "$output" >&2
  exit 1
fi
if [[ "$(<"$work/workflow-endpoint")" != "$workflow_endpoint" ]]; then
  printf 'workflow-run evidence was not bounded to the release commit\n' >&2
  exit 1
fi
