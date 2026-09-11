#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
readonly validator="$script_dir/validate-release-ref.sh"
readonly commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
readonly tag_oid=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

fixture() {
  local tag="${1:?tag required}"
  jq -cn \
    --arg tag "$tag" \
    --arg commit "$commit" \
    --arg tag_oid "$tag_oid" \
    '{
      event: {
        action: "release",
        client_payload: {tag: $tag}
      },
      ref: {
        ref: ("refs/tags/" + $tag),
        object: {type: "tag", sha: $tag_oid}
      },
      tag: {
        sha: $tag_oid,
        tag: $tag,
        object: {type: "commit", sha: $commit},
        verification: {verified: true, reason: "valid"}
      },
      commit: {
        sha: $commit,
        verification: {verified: true, reason: "valid"}
      },
      checks: {
        check_runs: [{
          name: "check",
          app: {id: 15368},
          status: "completed",
          conclusion: "success",
          details_url: "https://github.com/CtrlSpice/bargeboard/actions/runs/42/job/100",
          started_at: "2026-09-08T00:00:00Z"
        }]
      },
      workflow_runs: {
        workflow_runs: [{
          id: 42,
          path: ".github/workflows/check.yml",
          event: "push",
          head_branch: "main",
          head_sha: $commit,
          status: "completed",
          conclusion: "success"
        }]
      }
    }'
}

accept() {
  local tag="${1:?tag required}"
  if ! fixture "$tag" | bash "$validator" "$tag" "$commit"; then
    printf 'expected valid release evidence for %s\n' "$tag" >&2
    exit 1
  fi
}

reject_tag() {
  local tag="${1:?tag required}"
  if fixture "$tag" | bash "$validator" "$tag" "$commit" >/dev/null 2>&1; then
    printf 'expected invalid release tag: %s\n' "$tag" >&2
    exit 1
  fi
}

reject_evidence() {
  local description="${1:?description required}"
  local filter="${2:?jq filter required}"
  if fixture v1.2.3 | jq -c "$filter" | bash "$validator" v1.2.3 "$commit" >/dev/null 2>&1; then
    printf 'expected invalid release evidence: %s\n' "$description" >&2
    exit 1
  fi
}

accept v0.0.0
accept v1.2.3
accept v1.0.0-alpha
accept v1.0.0-alpha.1
accept v1.0.0-0.3.7
accept v1.0.0-x.7.z.92
accept v1.0.0-x-y-z.--
accept v1.0.0+build.1
accept v1.0.0-beta+exp.sha.5114f85
max_prerelease="$(printf 'a%.0s' {1..124})"
readonly max_prerelease
accept "v1.2.3-$max_prerelease"

reject_tag 1.2.3
reject_tag v1.2
reject_tag v01.2.3
reject_tag v1.02.3
reject_tag v1.2.03
reject_tag v1.2.3-
reject_tag v1.2.3-01
reject_tag v1.2.3-alpha..1
reject_tag v1.2.3+
reject_tag v1.2.3+build..1
reject_tag v1.2.3_alpha
reject_tag v1.2.3.4
reject_tag v1.2.3/other
reject_tag "v1.2.3-${max_prerelease}a"

if fixture v1.2.3 | bash "$validator" v1.2.3 cccccccccccccccccccccccccccccccccccccccc >/dev/null 2>&1; then
  printf 'expected a release tag away from current main to fail\n' >&2
  exit 1
fi

reject_evidence 'lightweight tag' '.ref.object.type = "commit"'
reject_evidence 'wrong dispatch action' '.event.action = "other"'
reject_evidence 'wrong dispatched tag' '.event.client_payload.tag = "v1.2.4"'
reject_evidence 'extra dispatch input' '.event.client_payload.unexpected = true'
reject_evidence 'ref and tag object mismatch' '.tag.sha = "cccccccccccccccccccccccccccccccccccccccc"'
reject_evidence 'tag name mismatch' '.tag.tag = "v1.2.4"'
reject_evidence 'unverified tag' '.tag.verification.verified = false'
reject_evidence 'invalid tag signature' '.tag.verification.reason = "bad_email"'
reject_evidence 'nested tag target' '.tag.object.type = "tag"'
reject_evidence 'commit response mismatch' '.commit.sha = "cccccccccccccccccccccccccccccccccccccccc"'
reject_evidence 'unverified commit' '.commit.verification.verified = false'
reject_evidence 'invalid commit signature' '.commit.verification.reason = "unsigned"'
reject_evidence 'missing required check' '.checks.check_runs = []'
reject_evidence 'wrong check app' '.checks.check_runs[0].app.id = 1'
reject_evidence 'wrong workflow path' '.workflow_runs.workflow_runs[0].path = ".github/workflows/release.yml"'
reject_evidence 'wrong workflow event' '.workflow_runs.workflow_runs[0].event = "pull_request"'
reject_evidence 'wrong workflow branch' '.workflow_runs.workflow_runs[0].head_branch = "feature"'
reject_evidence 'wrong workflow commit' '.workflow_runs.workflow_runs[0].head_sha = "cccccccccccccccccccccccccccccccccccccccc"'
reject_evidence 'different workflow run' '.workflow_runs.workflow_runs[0].id = 43'
reject_evidence 'failed workflow run' '.workflow_runs.workflow_runs[0].conclusion = "failure"'
reject_evidence 'duplicate workflow run' '.workflow_runs.workflow_runs += [.workflow_runs.workflow_runs[0]]'
reject_evidence 'pending required check' '.checks.check_runs[0].status = "in_progress"'
reject_evidence 'failed required check' '.checks.check_runs[0].conclusion = "failure"'
reject_evidence 'newer failed required check' '.checks.check_runs += [{name: "check", app: {id: 15368}, status: "completed", conclusion: "failure", started_at: "2026-09-08T00:01:00Z"}]'

fixture v1.2.3 \
  | jq -c '.checks.check_runs[0].conclusion = "failure" | .checks.check_runs += [{name: "check", app: {id: 15368}, status: "completed", conclusion: "success", details_url: "https://github.com/CtrlSpice/bargeboard/actions/runs/42/job/101", started_at: "2026-09-08T00:01:00Z"}]' \
  | bash "$validator" v1.2.3 "$commit"
