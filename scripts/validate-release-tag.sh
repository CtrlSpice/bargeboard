#!/usr/bin/env bash
set -euo pipefail

readonly tag="${1:?usage: validate-release-tag.sh TAG}"
readonly semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'
readonly uint64_max=18446744073709551615

fits_uint64() {
  local value="${1:?decimal value required}"
  local index digit limit_digit
  if (( ${#value} < ${#uint64_max} )); then
    return 0
  fi
  if (( ${#value} > ${#uint64_max} )); then
    return 1
  fi
  for ((index = 0; index < ${#uint64_max}; index++)); do
    digit="${value:index:1}"
    limit_digit="${uint64_max:index:1}"
    if (( 10#$digit < 10#$limit_digit )); then
      return 0
    fi
    if (( 10#$digit > 10#$limit_digit )); then
      return 1
    fi
  done
  return 0
}

if [[ ! "$tag" =~ $semver ]]; then
  printf 'release tag is not strict SemVer with a v prefix: %s\n' "$tag" >&2
  exit 1
fi
readonly major="${BASH_REMATCH[1]}"
readonly minor="${BASH_REMATCH[2]}"
readonly patch="${BASH_REMATCH[3]}"
if [[ "$major" != 0 && "$major" != 1 ]]; then
  printf 'release tag major version is incompatible with module path github.com/CtrlSpice/bargeboard: %s\n' "$tag" >&2
  exit 1
fi
if ! fits_uint64 "$minor" || ! fits_uint64 "$patch"; then
  printf 'release tag contains a version component outside GoReleaser uint64 bounds: %s\n' "$tag" >&2
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
