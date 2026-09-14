#!/usr/bin/env bash
set -euo pipefail

readonly resolver=scripts/go-toolchain.sh

assert_record() {
  local runner_os="${1:?runner OS required}"
  local runner_arch="${2:?runner architecture required}"
  local expected="${3:?expected record required}"
  local actual
  actual="$(bash "$resolver" "$runner_os" "$runner_arch")"
  if [[ "$actual" != "$expected" ]]; then
    printf 'unexpected Go toolchain record for %s/%s:\n%s\n' "$runner_os" "$runner_arch" "$actual" >&2
    exit 1
  fi
}

assert_record \
  Linux X64 \
  $'1.26.8\tgo1.26.8.linux-amd64.tar.gz\td0f743b33e8d8945e6b1f432edd15785c70507121d6e2a723b21285eddf8b57b'
assert_record \
  macOS X64 \
  $'1.26.8\tgo1.26.8.darwin-amd64.tar.gz\t186be014105aa6542b767d2c6ed5cca10a0214bdff809ef1724022a8c7894150'
assert_record \
  macOS ARM64 \
  $'1.26.8\tgo1.26.8.darwin-arm64.tar.gz\ta012b25b571bd0138a03dcd25375ceba866fe5ca822f426d2c66a4de56fd3f4b'
assert_record \
  Windows X64 \
  $'1.26.8\tgo1.26.8.windows-amd64.zip\tb92c3b2adae85a11ba71fe7216daf0d84e82af4c8ab6c5625807f28622043a59'

if bash "$resolver" Linux ARM64 >/dev/null 2>&1; then
  printf 'unsupported Go toolchain record was accepted\n' >&2
  exit 1
fi
