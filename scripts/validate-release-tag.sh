#!/usr/bin/env bash
set -euo pipefail

readonly tag="${1:?usage: validate-release-tag.sh TAG}"
readonly semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'

if [[ ! "$tag" =~ $semver ]]; then
  printf 'release tag is not strict SemVer with a v prefix: %s\n' "$tag" >&2
  exit 1
fi
readonly major="${BASH_REMATCH[1]}"
if [[ "$major" != 0 && "$major" != 1 ]]; then
  printf 'release tag major version is incompatible with module path github.com/CtrlSpice/bargeboard: %s\n' "$tag" >&2
  exit 1
fi
if [[ "$tag" == *+* ]]; then
  printf 'release tag build metadata cannot be represented in the Go module version: %s\n' "$tag" >&2
  exit 1
fi
if (( ${#tag} > 131 )); then
  printf 'release tag is too long for the canonical archive layout: %s\n' "$tag" >&2
  exit 1
fi
