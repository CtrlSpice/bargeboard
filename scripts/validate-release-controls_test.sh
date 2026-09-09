#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
readonly validator="$script_dir/validate-release-controls.sh"

fixture() {
  jq -cn '{
    environment: {
      name: "release",
      can_admins_bypass: false,
      protection_rules: [
        {type: "branch_policy"},
        {
          type: "required_reviewers",
          prevent_self_review: false,
          reviewers: [{type: "User", reviewer: {login: "CtrlSpice"}}]
        }
      ],
      deployment_branch_policy: {
        protected_branches: false,
        custom_branch_policies: true
      }
    },
    deployment_policies: {
      total_count: 1,
      branch_policies: [{name: "v*", type: "tag"}]
    },
    immutable_releases: {
      enabled: true,
      enforced_by_owner: false
    },
    immutability_ruleset: {
      name: "Protect release tags",
      target: "tag",
      enforcement: "active",
      bypass_actors: [],
      conditions: {
        ref_name: {include: ["refs/tags/v*"], exclude: []}
      },
      rules: [
        {type: "deletion"},
        {type: "required_signatures"},
        {type: "update"}
      ]
    },
    creation_ruleset: {
      name: "Restrict release tag creation",
      target: "tag",
      enforcement: "active",
      bypass_actors: [{
        actor_id: 56372758,
        actor_type: "User",
        bypass_mode: "always"
      }],
      conditions: {
        ref_name: {include: ["refs/tags/v*"], exclude: []}
      },
      rules: [{type: "creation"}]
    },
    main_ruleset: {
      name: "Protect main",
      target: "branch",
      enforcement: "active",
      bypass_actors: [],
      conditions: {
        ref_name: {include: ["refs/heads/main"], exclude: []}
      },
      rules: [
        {type: "deletion"},
        {type: "non_fast_forward"},
        {type: "required_linear_history"},
        {
          type: "pull_request",
          parameters: {
            allowed_merge_methods: ["squash"],
            dismiss_stale_reviews_on_push: false,
            require_code_owner_review: false,
            require_extra_approval_for_unattributed_changes: true,
            require_last_push_approval: false,
            required_approving_review_count: 0,
            required_review_thread_resolution: true,
            required_reviewers: []
          }
        },
        {
          type: "required_status_checks",
          parameters: {
            do_not_enforce_on_create: true,
            required_status_checks: [{context: "check", integration_id: 15368}],
            strict_required_status_checks_policy: true
          }
        }
      ]
    }
  }'
}

reject() {
  local description="${1:?description required}"
  local filter="${2:?jq filter required}"
  if fixture | jq -c "$filter" | bash "$validator" >/dev/null 2>&1; then
    printf 'expected invalid release controls: %s\n' "$description" >&2
    exit 1
  fi
}

fixture | bash "$validator"

reject 'administrator bypass' '.environment.can_admins_bypass = true'
reject 'prevented self review' '.environment.protection_rules[1].prevent_self_review = true'
reject 'extra reviewer' '.environment.protection_rules[1].reviewers += [{type: "User", reviewer: {login: "Other"}}]'
reject 'team reviewer' '.environment.protection_rules[1].reviewers = [{type: "Team", reviewer: {login: "maintainers"}}]'
reject 'extra protection rule' '.environment.protection_rules += [{type: "wait_timer"}]'
reject 'protected branch policy' '.environment.deployment_branch_policy.protected_branches = true'
reject 'extra deployment policy' '.deployment_policies.branch_policies += [{name: "main", type: "branch"}]'
reject 'incorrect deployment-policy count' '.deployment_policies.total_count = 2'
reject 'disabled immutable releases' '.immutable_releases.enabled = false'
reject 'missing owner-enforcement evidence' 'del(.immutable_releases.enforced_by_owner)'
reject 'missing bypass evidence' 'del(.immutability_ruleset.bypass_actors)'
reject 'null bypass evidence' '.immutability_ruleset.bypass_actors = null'
reject 'immutability bypass actor' '.immutability_ruleset.bypass_actors = [{actor_type: "RepositoryRole", actor_id: 5}]'
reject 'wrong immutable tag pattern' '.immutability_ruleset.conditions.ref_name.include = ["refs/tags/release-*"]'
reject 'excluded immutable tag' '.immutability_ruleset.conditions.ref_name.exclude = ["refs/tags/v0.*"]'
reject 'missing signature rule' '.immutability_ruleset.rules = [.immutability_ruleset.rules[] | select(.type != "required_signatures")]'
reject 'extra immutability rule' '.immutability_ruleset.rules += [{type: "creation"}]'
reject 'missing creator' '.creation_ruleset.bypass_actors = []'
reject 'wrong creator' '.creation_ruleset.bypass_actors[0].actor_id = 1'
reject 'extra creator' '.creation_ruleset.bypass_actors += [{actor_id: 1, actor_type: "User", bypass_mode: "always"}]'
reject 'wrong creation tag pattern' '.creation_ruleset.conditions.ref_name.include = ["refs/tags/release-*"]'
reject 'missing creation rule' '.creation_ruleset.rules = []'
reject 'main ruleset bypass' '.main_ruleset.bypass_actors = [{actor_type: "RepositoryRole", actor_id: 5}]'
reject 'wrong main branch' '.main_ruleset.conditions.ref_name.include = ["refs/heads/trunk"]'
reject 'main merge commits' '.main_ruleset.rules[3].parameters.allowed_merge_methods = ["merge", "squash"]'
reject 'unresolved conversations' '.main_ruleset.rules[3].parameters.required_review_thread_resolution = false'
reject 'unattributed changes' '.main_ruleset.rules[3].parameters.require_extra_approval_for_unattributed_changes = false'
reject 'non-strict status checks' '.main_ruleset.rules[4].parameters.strict_required_status_checks_policy = false'
reject 'wrong required check app' '.main_ruleset.rules[4].parameters.required_status_checks[0].integration_id = 1'
