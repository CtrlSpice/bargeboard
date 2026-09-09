#!/usr/bin/env bash
set -euo pipefail

payload="$(jq -ce .)" || {
  printf 'release-control evidence is not valid JSON\n' >&2
  exit 1
}
readonly payload

if ! jq -e '
  .environment.name == "release" and
  .environment.can_admins_bypass == false and
  .environment.deployment_branch_policy.protected_branches == false and
  .environment.deployment_branch_policy.custom_branch_policies == true and
  ([
    .environment.protection_rules[]
    | select(.type == "required_reviewers")
    | {
        prevent_self_review,
        reviewers: [.reviewers[] | {type, login: .reviewer.login}]
      }
  ] == [{
    prevent_self_review: false,
    reviewers: [{type: "User", login: "CtrlSpice"}]
  }]) and
  ([.environment.protection_rules[].type] | sort) == ["branch_policy", "required_reviewers"] and
  .deployment_policies.total_count == 1 and
  ([.deployment_policies.branch_policies[] | {name, type}]) == [{name: "v*", type: "tag"}] and
  .immutable_releases.enabled == true and
  (.immutable_releases.enforced_by_owner | type) == "boolean" and
  .immutability_ruleset.name == "Protect release tags" and
  .immutability_ruleset.target == "tag" and
  .immutability_ruleset.enforcement == "active" and
  .immutability_ruleset.bypass_actors == [] and
  .immutability_ruleset.conditions.ref_name.include == ["refs/tags/v*"] and
  .immutability_ruleset.conditions.ref_name.exclude == [] and
  ([.immutability_ruleset.rules[].type] | sort) == ["deletion", "required_signatures", "update"] and
  .creation_ruleset.name == "Restrict release tag creation" and
  .creation_ruleset.target == "tag" and
  .creation_ruleset.enforcement == "active" and
  ([.creation_ruleset.bypass_actors[] | {actor_id, actor_type, bypass_mode}]) == [{
    actor_id: 56372758,
    actor_type: "User",
    bypass_mode: "always"
  }] and
  .creation_ruleset.conditions.ref_name.include == ["refs/tags/v*"] and
  .creation_ruleset.conditions.ref_name.exclude == [] and
  [.creation_ruleset.rules[].type] == ["creation"] and
  .main_ruleset.name == "Protect main" and
  .main_ruleset.target == "branch" and
  .main_ruleset.enforcement == "active" and
  .main_ruleset.bypass_actors == [] and
  .main_ruleset.conditions.ref_name.include == ["refs/heads/main"] and
  .main_ruleset.conditions.ref_name.exclude == [] and
  ([.main_ruleset.rules[].type] | sort) == [
    "deletion",
    "non_fast_forward",
    "pull_request",
    "required_linear_history",
    "required_status_checks"
  ] and
  ([.main_ruleset.rules[] | select(.type == "pull_request") | .parameters | {
    allowed_merge_methods,
    dismiss_stale_reviews_on_push,
    require_code_owner_review,
    require_extra_approval_for_unattributed_changes,
    require_last_push_approval,
    required_approving_review_count,
    required_review_thread_resolution,
    required_reviewers
  }]) == [{
    allowed_merge_methods: ["squash"],
    dismiss_stale_reviews_on_push: false,
    require_code_owner_review: false,
    require_extra_approval_for_unattributed_changes: true,
    require_last_push_approval: false,
    required_approving_review_count: 0,
    required_review_thread_resolution: true,
    required_reviewers: []
  }] and
  ([.main_ruleset.rules[] | select(.type == "required_status_checks") | .parameters]) == [{
    do_not_enforce_on_create: true,
    required_status_checks: [{context: "check", integration_id: 15368}],
    strict_required_status_checks_policy: true
  }]
' <<<"$payload" >/dev/null; then
  printf 'release controls do not match policy\n' >&2
  exit 1
fi
