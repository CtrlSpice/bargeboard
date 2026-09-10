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
temporary_binary="$(mktemp "${document}.binary.XXXXXX")"
readonly temporary_binary
trap 'rm -f "$temporary_document" "$temporary_binary"' EXIT

artifact_name="${artifact##*/}"
case "$artifact_name" in
  *.tar.gz)
    archive_root="${artifact_name%.tar.gz}"
    tar -xOzf "$artifact" "$archive_root/bargeboard" >"$temporary_binary"
    ;;
  *.zip)
    archive_root="${artifact_name%.zip}"
    unzip -p "$artifact" "$archive_root/bargeboard.exe" >"$temporary_binary"
    ;;
  *)
    printf 'unsupported SBOM artifact: %s\n' "$artifact" >&2
    exit 1
    ;;
esac
readonly archive_root

module_version="$(
  go version -m -json "$temporary_binary" |
    jq -er --arg package "$project_package" '
      select(.Path == $package and .Main.Path == $package) |
      .Main.Version | select(type == "string" and length > 0)
    '
)"
readonly module_version

syft "$artifact" \
  --override-default-catalogers go-module-binary-cataloger \
  --output "spdx-json=$temporary_document"

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
  --arg package "$project_package" \
  --arg module_version "$module_version" '
    .documentNamespace = $namespace |
    .creationInfo.created = $created |
    (.packages[] | select(.name == $package)) |= (
      .versionInfo = $module_version |
      .licenseConcluded = "Apache-2.0" |
      .licenseDeclared = "Apache-2.0"
    ) |
    (.packages[] | select(
      .name != "stdlib" and
      any(.externalRefs[]?; .referenceType == "purl")
    )) |= (
      .externalRefs = (
        [.externalRefs[]? | select(.referenceType != "purl")] + [{
          referenceCategory: "PACKAGE-MANAGER",
          referenceType: "purl",
          referenceLocator: (
            "pkg:golang/" + (.name | ascii_downcase) + "@" + (.versionInfo | @uri)
          )
        }]
      )
    )
  ' "$temporary_document" >"$document"
