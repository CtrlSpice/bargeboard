#!/usr/bin/env bash
set -euo pipefail

readonly verifier=scripts/verify-sha256.sh
work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT

printf 'archive fixture\n' >"$work/archive"
readonly digest=759ad2ffeb54eec4165fc63d0a8d9cd4c592d314b87840a6a6c9b1156c41d601
bash "$verifier" "$work/archive" "$digest"

if bash "$verifier" "$work/archive" "${digest%?}0" >/dev/null 2>&1; then
  printf 'mismatched archive digest was accepted\n' >&2
  exit 1
fi
if bash "$verifier" "$work/archive" invalid >/dev/null 2>&1; then
  printf 'malformed archive digest was accepted\n' >&2
  exit 1
fi
ln -s archive "$work/archive-link"
if bash "$verifier" "$work/archive-link" "$digest" >/dev/null 2>&1; then
  printf 'symbolic-link checksum subject was accepted\n' >&2
  exit 1
fi
