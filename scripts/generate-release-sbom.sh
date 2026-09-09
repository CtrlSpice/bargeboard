#!/usr/bin/env bash
set -euo pipefail

readonly artifact="${1:?usage: generate-release-sbom.sh ARTIFACT DOCUMENT}"
readonly document="${2:?usage: generate-release-sbom.sh ARTIFACT DOCUMENT}"
readonly project_package=github.com/CtrlSpice/bargeboard

if [[ ! -f "$artifact" ]]; then
  printf 'SBOM artifact does not exist: %s\n' "$artifact" >&2
  exit 1
fi

temporary_document="$(mktemp "${document}.tmp.XXXXXX")"
readonly temporary_document
trap 'rm -f "$temporary_document"' EXIT

syft "$artifact" --output "spdx-json=$temporary_document"

if command -v sha256sum >/dev/null 2>&1; then
  artifact_digest="$(sha256sum "$artifact" | cut -d ' ' -f 1)"
else
  artifact_digest="$(shasum -a 256 "$artifact" | cut -d ' ' -f 1)"
fi
readonly artifact_digest
readonly document_namespace="https://github.com/CtrlSpice/bargeboard/sbom/sha256-$artifact_digest"
commit_epoch="$(git show -s --format=%ct HEAD)"
readonly commit_epoch
created="$(jq -nr --argjson epoch "$commit_epoch" '$epoch | todateiso8601')"
readonly created

if ! jq -e --arg package "$project_package" '
  [.packages[] | select(.name == $package)] | length == 1
' "$temporary_document" >/dev/null; then
  printf 'SBOM does not contain exactly one bargeboard package: %s\n' "$artifact" >&2
  exit 1
fi

jq \
  --arg namespace "$document_namespace" \
  --arg created "$created" \
  --arg package "$project_package" '
    .documentNamespace = $namespace |
    .creationInfo.created = $created |
    (.packages[] | select(.name == $package) | .licenseConcluded) = "Apache-2.0" |
    (.packages[] | select(.name == $package) | .licenseDeclared) = "Apache-2.0"
  ' "$temporary_document" >"$document"
