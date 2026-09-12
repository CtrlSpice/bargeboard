#!/usr/bin/env bash
set -euo pipefail

file="${1:?usage: verify-sha256.sh FILE EXPECTED_SHA256}"
readonly expected="${2:?usage: verify-sha256.sh FILE EXPECTED_SHA256}"

case "$file" in
  [A-Za-z]:\\*) file="$(cygpath -u "$file")" ;;
esac
readonly file

if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  printf 'invalid expected SHA-256 digest: %s\n' "$expected" >&2
  exit 1
fi
if [[ ! -f "$file" || -L "$file" ]]; then
  printf 'checksum subject is not a regular file: %s\n' "$file" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$file" | cut -d ' ' -f 1)"
else
  actual="$(shasum -a 256 "$file" | cut -d ' ' -f 1)"
fi
readonly actual

if [[ "$actual" != "$expected" ]]; then
  printf 'SHA-256 mismatch for %s: expected %s, got %s\n' "$file" "$expected" "$actual" >&2
  exit 1
fi
