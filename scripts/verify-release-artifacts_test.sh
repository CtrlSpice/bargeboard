#!/usr/bin/env bash
set -euo pipefail

source_dist="${1:-build/release}"
source_dist="$(cd -- "$source_dist" && pwd)"
readonly source_dist
readonly verifier=scripts/verify-release-artifacts.sh
work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT

expect_failure() {
  local description="${1:?description required}"
  local expected_message="${2:?expected message required}"
  shift 2
  local output
  if output="$(bash "$verifier" "$@" 2>&1)"; then
    printf 'expected release artifact verification failure: %s\n' "$description" >&2
    exit 1
  fi
  if ! grep -F "$expected_message" <<<"$output" >/dev/null; then
    printf 'release artifact failure did not reach %s:\n%s\n' "$description" "$output" >&2
    exit 1
  fi
}

refresh_checksum() {
  local dist="${1:?distribution required}"
  local file="${2:?file required}"
  local name digest
  name="$(basename "$file")"
  if command -v sha256sum >/dev/null 2>&1; then
    digest="$(sha256sum "$file" | cut -d ' ' -f 1)"
  else
    digest="$(shasum -a 256 "$file" | cut -d ' ' -f 1)"
  fi
  awk -v name="$name" -v digest="$digest" '
    $2 == name { print digest "  " name; next }
    { print }
  ' "$dist/checksums.txt" >"$work/checksums.txt"
  mv "$work/checksums.txt" "$dist/checksums.txt"
}

mutate_sbom() {
  local dist="${1:?distribution required}"
  local filter="${2:?jq filter required}"
  local sboms sbom
  sboms=("$dist"/*.sbom.spdx.json)
  sbom="${sboms[0]}"
  jq "$filter" "$sbom" >"$work/mutated-sbom.json"
  mv "$work/mutated-sbom.json" "$sbom"
  refresh_checksum "$dist" "$sbom"
}

bash "$verifier" "$source_dist"

cp -R "$source_dist" "$work/dist-extra-file"
unexpected="$work/dist-extra-file/unexpected.tar.gz"
touch "$unexpected"
expect_failure 'root file cardinality' 'release output contains unexpected or missing root files' "$work/dist-extra-file"

cp -R "$source_dist" "$work/dist-extra-subject"
printf '%064d  unexpected.tar.gz\n' 0 >>"$work/dist-extra-subject/checksums.txt"
expect_failure 'checksum subject cardinality' 'checksums.txt does not contain exactly' "$work/dist-extra-subject"

cp -R "$source_dist" "$work/dist-invalid-sbom"
mutate_sbom "$work/dist-invalid-sbom" '.spdxVersion = "SPDX-0.0"'
expect_failure 'SPDX structure' 'invalid release SPDX document' "$work/dist-invalid-sbom"

cp -R "$source_dist" "$work/dist-wrong-sbom-version"
mutate_sbom "$work/dist-wrong-sbom-version" \
  '(.packages[] | select(.name == "github.com/CtrlSpice/bargeboard") | .versionInfo) = "9.9.9"'
expect_failure \
  'SPDX project version' \
  'SBOM project package does not match binary module identity' \
  "$work/dist-wrong-sbom-version"

cp -R "$source_dist" "$work/dist-wrong-project-purl"
mutate_sbom "$work/dist-wrong-project-purl" '
  (.packages[] |
    select(.name == "github.com/CtrlSpice/bargeboard") |
    .externalRefs[] |
    select(.referenceType == "purl") |
    .referenceLocator) = "pkg:golang/github.com/ctrlspice/other@v1.0.0"
'
expect_failure \
  'SPDX project PURL' \
  'SBOM project package does not match binary module identity' \
  "$work/dist-wrong-project-purl"

cp -R "$source_dist" "$work/dist-wrong-project-source"
mutate_sbom "$work/dist-wrong-project-source" '
  (.packages[] |
    select(.name == "github.com/CtrlSpice/bargeboard") |
    .sourceInfo) = "acquired package info from an unrelated binary"
'
expect_failure \
  'SPDX project source' \
  'SBOM project package does not match binary module identity' \
  "$work/dist-wrong-project-source"

cp -R "$source_dist" "$work/dist-incomplete-sbom"
mutate_sbom "$work/dist-incomplete-sbom" '
  (.packages[] | select(.name == "google.golang.org/grpc") | .SPDXID) as $id |
  .packages = [.packages[] | select(.SPDXID != $id)] |
  .relationships = [.relationships[] | select(
    .spdxElementId != $id and .relatedSpdxElement != $id
  )]
'
expect_failure \
  'SPDX package inventory' \
  'SBOM package inventory does not match binary build info' \
  "$work/dist-incomplete-sbom"

cp -R "$source_dist" "$work/dist-missing-package-field"
mutate_sbom "$work/dist-missing-package-field" '
  del(.packages[] | select(.name == "google.golang.org/grpc") | .downloadLocation)
'
expect_failure \
  'required SPDX package metadata' \
  'invalid release SPDX document' \
  "$work/dist-missing-package-field"

cp -R "$source_dist" "$work/dist-wrong-dependency-checksum"
mutate_sbom "$work/dist-wrong-dependency-checksum" '
  (.packages[] |
    select(.name == "google.golang.org/grpc") |
    .checksums[] |
    select(.algorithm == "SHA256") |
    .checksumValue) =
      "0000000000000000000000000000000000000000000000000000000000000000"
'
expect_failure \
  'authenticated dependency checksum' \
  'SBOM dependency does not match authenticated Go module metadata' \
  "$work/dist-wrong-dependency-checksum"

cp -R "$source_dist" "$work/dist-wrong-dependency-purl"
mutate_sbom "$work/dist-wrong-dependency-purl" '
  (.packages[] |
    select(.name == "google.golang.org/grpc") |
    .externalRefs[] |
    select(.referenceType == "purl") |
    .referenceLocator) = "pkg:golang/google.golang.org/grpc@v0.0.0"
'
expect_failure \
  'SPDX dependency PURL' \
  'SBOM dependency does not match authenticated Go module metadata' \
  "$work/dist-wrong-dependency-purl"

cp -R "$source_dist" "$work/dist-wrong-dependency-source"
mutate_sbom "$work/dist-wrong-dependency-source" '
  (.packages[] | select(.name == "google.golang.org/grpc") | .sourceInfo) =
    "acquired package info from an unrelated file"
'
expect_failure \
  'SPDX dependency source' \
  'SBOM dependency does not match authenticated Go module metadata' \
  "$work/dist-wrong-dependency-source"

cp -R "$source_dist" "$work/dist-incomplete-relationships"
mutate_sbom "$work/dist-incomplete-relationships" '
  (.packages[] | select(.name == "google.golang.org/grpc") | .SPDXID) as $id |
  .relationships = [.relationships[] | select(
    .spdxElementId != $id or .relationshipType != "DEPENDENCY_OF"
  )]
'
expect_failure \
  'SPDX dependency relationships' \
  'SBOM relationships do not match binary package ownership' \
  "$work/dist-incomplete-relationships"

cp -R "$source_dist" "$work/dist-extra-relationship"
mutate_sbom "$work/dist-extra-relationship" '
  ([.packages[] | select(.primaryPackagePurpose == "FILE")][0].SPDXID) as $root |
  .relationships += [{
    spdxElementId: $root,
    relationshipType: "OTHER",
    relatedSpdxElement: .files[0].SPDXID
  }]
'
expect_failure \
  'closed SPDX relationships' \
  'SBOM relationships do not match binary package ownership' \
  "$work/dist-extra-relationship"

cp -R "$source_dist" "$work/dist-wrong-file-checksum"
mutate_sbom "$work/dist-wrong-file-checksum" '
  (.files[0].checksums[] | select(.algorithm == "SHA256") | .checksumValue) =
    "0000000000000000000000000000000000000000000000000000000000000000"
'
expect_failure \
  'SPDX binary checksum' \
  'SBOM binary file does not match archive payload' \
  "$work/dist-wrong-file-checksum"

cp -R "$source_dist" "$work/dist-tampered-archive"
source_archives=("$source_dist"/*.tar.gz)
archive="$work/dist-tampered-archive/${source_archives[0]##*/}"
printf 'tampered\n' >>"$archive"
expect_failure 'archive checksum' 'FAILED' "$work/dist-tampered-archive"

mkdir "$work/source"
cp LICENSE README.md config.yaml "$work/source/"
printf 'tampered\n' >>"$work/source/LICENSE"
expect_failure 'tracked payload equality' 'archive payload differs from tracked LICENSE' "$source_dist" "$work/source"

cp -R "$source_dist" "$work/dist-invalid-metadata"
jq 'map(if .type == "Archive" and .target == "linux_amd64_v1" then .target = "linux_amd64_v3" else . end)' \
  "$work/dist-invalid-metadata/artifacts.json" >"$work/artifacts.json"
mv "$work/artifacts.json" "$work/dist-invalid-metadata/artifacts.json"
expect_failure 'artifact metadata correlation' 'archive targets do not match the supported release matrix' "$work/dist-invalid-metadata"

mkdir "$work/no-version-bin"
printf '#!/usr/bin/env bash\nprintf "v0.0.0\\n"\n' >"$work/no-version-bin/strings"
chmod +x "$work/no-version-bin/strings"
PATH="$work/no-version-bin:$PATH" expect_failure \
  'embedded release version' \
  'archive binary does not contain release version' \
  "$source_dist"

bash "$verifier" "$source_dist"
