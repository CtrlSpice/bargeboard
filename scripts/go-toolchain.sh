#!/usr/bin/env bash
set -euo pipefail

runner_os="${1:?runner OS required}"
runner_arch="${2:?runner architecture required}"

readonly version="${3:-1.26.8}"

case "$version/$runner_os/$runner_arch" in
  1.26.8/Linux/X64)
    archive="go${version}.linux-amd64.tar.gz"
    checksum=d0f743b33e8d8945e6b1f432edd15785c70507121d6e2a723b21285eddf8b57b
    ;;
  1.26.8/macOS/X64)
    archive="go${version}.darwin-amd64.tar.gz"
    checksum=186be014105aa6542b767d2c6ed5cca10a0214bdff809ef1724022a8c7894150
    ;;
  1.26.8/macOS/ARM64)
    archive="go${version}.darwin-arm64.tar.gz"
    checksum=a012b25b571bd0138a03dcd25375ceba866fe5ca822f426d2c66a4de56fd3f4b
    ;;
  1.26.8/Windows/X64)
    archive="go${version}.windows-amd64.zip"
    checksum=b92c3b2adae85a11ba71fe7216daf0d84e82af4c8ab6c5625807f28622043a59
    ;;
  1.27.1/Linux/X64)
    archive="go${version}.linux-amd64.tar.gz"
    checksum=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
    ;;
  *)
    printf 'unsupported GitHub runner for Go %s: %s/%s\n' "$version" "$runner_os" "$runner_arch" >&2
    exit 1
    ;;
esac

printf '%s\t%s\t%s\n' "$version" "$archive" "$checksum"
