#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 0 ]]; then
  printf 'usage: resolve-release-version.sh\n' >&2
  exit 1
fi
# Release versions come from the separately validated signed tag. This helper
# resolves only snapshot versions, including repositories with non-SemVer tags.
goreleaser build --snapshot --clean --config .goreleaser-version.yaml >&2
commit="$(git rev-parse --verify 'HEAD^{commit}')"
version="$(jq -er --arg commit "$commit" '
  select(.commit == $commit and .project_name == "bargeboard") |
  .version | select(type == "string" and length > 0)
' build/native-version/metadata.json)"
if [[ ! "$version" =~ ^[0-9A-Za-z][0-9A-Za-z.+-]*$ || ${#version} -gt 130 ]]; then
  printf 'invalid resolved release version: %s\n' "$version" >&2
  exit 1
fi
printf '%s\n' "$version"
