#!/usr/bin/env bash
set -euo pipefail

readonly resolver=scripts/go-toolchain.sh

assert_record() {
  local runner_os="${1:?runner OS required}"
  local runner_arch="${2:?runner architecture required}"
  local expected="${3:?expected record required}"
  local requested_version="${4:-}"
  local actual
  if [[ -n "$requested_version" ]]; then
    actual="$(bash "$resolver" "$runner_os" "$runner_arch" "$requested_version")"
  else
    actual="$(bash "$resolver" "$runner_os" "$runner_arch")"
  fi
  if [[ "$actual" != "$expected" ]]; then
    printf 'unexpected Go toolchain record for %s/%s at version %s:\n%s\n' \
      "$runner_os" "$runner_arch" "${requested_version:-default}" "$actual" >&2
    exit 1
  fi
}

assert_rejected() {
  if bash "$resolver" "$@" >/dev/null 2>&1; then
    printf 'unsupported Go toolchain record was accepted: %s\n' "$*" >&2
    exit 1
  fi
}

assert_record \
  Linux X64 \
  $'1.27.4\tgo1.27.4.linux-amd64.tar.gz\t7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840'
assert_record \
  macOS X64 \
  $'1.27.4\tgo1.27.4.darwin-amd64.tar.gz\t2b799377704ef12db39bd254995d5c348c9cc8d86562ca817724390e56c38f43'
assert_record \
  macOS ARM64 \
  $'1.27.4\tgo1.27.4.darwin-arm64.tar.gz\t91a44a2b8a716600ad94a4f143e156116620db8828086a6e3602291be9bd0bb4'
assert_record \
  Windows X64 \
  $'1.27.4\tgo1.27.4.windows-amd64.zip\t178ab98950ac69ab25069307dc6ade79f3816e414cce95b4da166a115cde1e7e'
assert_record \
  Linux X64 \
  $'1.27.4\tgo1.27.4.linux-amd64.tar.gz\t7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840' \
  1.27.4

assert_rejected Linux ARM64
assert_rejected macOS ARM64 1.27.1
assert_rejected Linux X64 1.27.0
assert_rejected Linux X64 1.26.8
assert_rejected Linux X64 latest
assert_rejected Windows ARM64
