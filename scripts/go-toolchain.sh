#!/usr/bin/env bash
set -euo pipefail

runner_os="${1:?runner OS required}"
runner_arch="${2:?runner architecture required}"

readonly version="${3:-1.27.4}"

case "$version/$runner_os/$runner_arch" in
  1.27.4/Linux/X64)
    archive="go${version}.linux-amd64.tar.gz"
    checksum=7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840
    ;;
  1.27.4/Linux/ARM64)
    archive="go${version}.linux-arm64.tar.gz"
    checksum=3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92
    ;;
  1.27.4/macOS/ARM64)
    archive="go${version}.darwin-arm64.tar.gz"
    checksum=91a44a2b8a716600ad94a4f143e156116620db8828086a6e3602291be9bd0bb4
    ;;
  1.27.4/Windows/X64)
    archive="go${version}.windows-amd64.zip"
    checksum=178ab98950ac69ab25069307dc6ade79f3816e414cce95b4da166a115cde1e7e
    ;;
  *)
    printf 'unsupported GitHub runner for Go %s: %s/%s\n' "$version" "$runner_os" "$runner_arch" >&2
    exit 1
    ;;
esac

printf '%s\t%s\t%s\n' "$version" "$archive" "$checksum"
