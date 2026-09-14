#!/usr/bin/env bash
set -euo pipefail

readonly expected_repository=CtrlSpice/bargeboard
readonly repository="${GITHUB_REPOSITORY:-$expected_repository}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir

if [[ "$repository" != "$expected_repository" ]]; then
  printf 'release controls are scoped to %s, not %s\n' "$expected_repository" "$repository" >&2
  exit 1
fi

environment="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/environments/release")"
readonly environment

deployment_policies="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/environments/release/deployment-branch-policies?per_page=100")"
readonly deployment_policies
immutable_releases="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/immutable-releases")"
readonly immutable_releases

rulesets="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/rulesets?per_page=100&targets=tag")"
readonly rulesets
immutability_ruleset_id="$(jq -er '[.[] | select(.name == "Protect release tags" and .target == "tag" and .enforcement == "active")] | select(length == 1) | .[0].id' <<<"$rulesets")"
readonly immutability_ruleset_id
creation_ruleset_id="$(jq -er '[.[] | select(.name == "Restrict release tag creation" and .target == "tag" and .enforcement == "active")] | select(length == 1) | .[0].id' <<<"$rulesets")"
readonly creation_ruleset_id
immutability_ruleset="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/rulesets/$immutability_ruleset_id")"
readonly immutability_ruleset
creation_ruleset="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/rulesets/$creation_ruleset_id")"
readonly creation_ruleset
main_ruleset_id="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/rulesets?per_page=100&targets=branch" \
  | jq -er '[.[] | select(.name == "Protect main" and .target == "branch" and .enforcement == "active")] | select(length == 1) | .[0].id')"
readonly main_ruleset_id
main_ruleset="$(gh api -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/$repository/rulesets/$main_ruleset_id")"
readonly main_ruleset

jq -cn \
  --argjson environment "$environment" \
  --argjson deployment_policies "$deployment_policies" \
  --argjson immutable_releases "$immutable_releases" \
  --argjson immutability_ruleset "$immutability_ruleset" \
  --argjson creation_ruleset "$creation_ruleset" \
  --argjson main_ruleset "$main_ruleset" \
  '{
    environment: $environment,
    deployment_policies: $deployment_policies,
    immutable_releases: $immutable_releases,
    immutability_ruleset: $immutability_ruleset,
    creation_ruleset: $creation_ruleset,
    main_ruleset: $main_ruleset
  }' | bash "$script_dir/validate-release-controls.sh"
