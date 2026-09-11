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

source_version="$(jq -er '.version' "$source_dist/metadata.json")"
readonly source_version
mismatched_tag=v0.0.0
if [[ "$source_version" == "${mismatched_tag#v}" ]]; then
  mismatched_tag=v0.0.1
fi
readonly mismatched_tag
if output="$(EXPECTED_TAG="$mismatched_tag" bash "$verifier" "$source_dist" 2>&1)"; then
  printf 'expected release artifact verification failure: release tag binding\n' >&2
  exit 1
fi
if ! grep -F 'release metadata version does not match release tag' <<<"$output" >/dev/null; then
  printf 'release artifact failure did not reach release tag binding:\n%s\n' "$output" >&2
  exit 1
fi

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

cp -R "$source_dist" "$work/dist-unknown-spdx-field"
mutate_sbom "$work/dist-unknown-spdx-field" '.unexpected = "field"'
expect_failure 'closed SPDX profile' 'invalid release SPDX document' "$work/dist-unknown-spdx-field"

cp -R "$source_dist" "$work/dist-invalid-package-purpose"
mutate_sbom "$work/dist-invalid-package-purpose" '
  (.packages[] | select(.name == "google.golang.org/grpc") | .primaryPackagePurpose) =
    "NOT-A-PURPOSE"
'
expect_failure 'SPDX package purpose' 'invalid release SPDX document' "$work/dist-invalid-package-purpose"

cp -R "$source_dist" "$work/dist-false-external-reference"
mutate_sbom "$work/dist-false-external-reference" '
  (.packages[] | select(.name == "google.golang.org/grpc") | .externalRefs) += [{
    referenceCategory: "SECURITY",
    referenceType: "cpe23Type",
    referenceLocator: "cpe:2.3:a:unrelated:package:1.0.0:*:*:*:*:*:*:*"
  }]
'
expect_failure 'unverified SPDX external reference' 'invalid release SPDX document' "$work/dist-false-external-reference"

cp -R "$source_dist" "$work/dist-invalid-archive-license"
mutate_sbom "$work/dist-invalid-archive-license" '
  (.packages[] | select(.primaryPackagePurpose == "ARCHIVE") | .licenseDeclared) =
    "NOT-A-LICENSE"
'
expect_failure 'SPDX archive license' 'invalid release SPDX document' "$work/dist-invalid-archive-license"

cp -R "$source_dist" "$work/dist-archive-purl"
mutate_sbom "$work/dist-archive-purl" '
  (.packages[] | select(.primaryPackagePurpose == "ARCHIVE") | .externalRefs) = [{
    referenceCategory: "PACKAGE-MANAGER",
    referenceType: "purl",
    referenceLocator: "not-a-purl"
  }]
'
expect_failure 'SPDX archive package shape' 'invalid release SPDX document' "$work/dist-archive-purl"

cp -R "$source_dist" "$work/dist-dependency-purpose"
mutate_sbom "$work/dist-dependency-purpose" '
  (.packages[] | select(.name == "google.golang.org/grpc") | .primaryPackagePurpose) = "ARCHIVE"
'
expect_failure \
  'SPDX dependency package shape' \
  'SBOM dependency does not match authenticated Go module metadata' \
  "$work/dist-dependency-purpose"

cp -R "$source_dist" "$work/dist-invalid-spdx-id"
mutate_sbom "$work/dist-invalid-spdx-id" '
  .files[0].SPDXID as $original |
  .files[0].SPDXID = "SPDXRef-invalid id" |
  .relationships |= map(
    if .spdxElementId == $original then .spdxElementId = "SPDXRef-invalid id"
    elif .relatedSpdxElement == $original then .relatedSpdxElement = "SPDXRef-invalid id"
    else . end
  )
'
expect_failure 'SPDX element ID grammar' 'invalid release SPDX document' "$work/dist-invalid-spdx-id"

cp -R "$source_dist" "$work/dist-relationship-comment"
mutate_sbom "$work/dist-relationship-comment" '
  (.relationships[] | select(.relationshipType == "OTHER") | .comment) = "unverified claim"
'
expect_failure 'SPDX relationship meaning' 'invalid release SPDX document' "$work/dist-relationship-comment"

cp -R "$source_dist" "$work/dist-stdlib-source"
mutate_sbom "$work/dist-stdlib-source" '
  (.packages[] | select(.name == "stdlib") | .sourceInfo) = "unrelated source"
'
expect_failure 'SPDX stdlib source' 'invalid release SPDX document' "$work/dist-stdlib-source"

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
  ([.packages[] | select(.primaryPackagePurpose == "ARCHIVE")][0].SPDXID) as $root |
  .relationships += [{
    spdxElementId: "SPDXRef-DOCUMENT",
    relationshipType: "DESCRIBES",
    relatedSpdxElement: $root
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

cp -R "$source_dist" "$work/dist-invalid-checksum"
awk 'NR == 1 { print sprintf("%064d", 0) "  " $2; next } { print }' \
  "$work/dist-invalid-checksum/checksums.txt" >"$work/checksums.txt"
mv "$work/checksums.txt" "$work/dist-invalid-checksum/checksums.txt"
expect_failure 'archive checksum' 'FAILED' "$work/dist-invalid-checksum"

cp -R "$source_dist" "$work/dist-tampered-archive"
source_archives=("$source_dist"/*.tar.gz)
archive="$work/dist-tampered-archive/${source_archives[0]##*/}"
printf 'tampered\n' >>"$archive"
expect_failure \
  'archive envelope preflight' \
  'archive metadata does not match release policy' \
  "$work/dist-tampered-archive"

mkdir "$work/source"
cp LICENSE README.md config.yaml "$work/source/"
printf 'tampered\n' >>"$work/source/LICENSE"
expect_failure 'tracked payload equality' 'archive payload differs from tracked LICENSE' "$source_dist" "$work/source"

mkdir "$work/mismatched-compliance-bin"
real_cmp="$(command -v cmp)"
cat >"$work/mismatched-compliance-bin/cmp" <<EOF
#!/usr/bin/env bash
if [[ "\${2:-}" == */build/compliance/THIRD_PARTY_NOTICES ]]; then
  exit 1
fi
exec "$real_cmp" "\$@"
EOF
chmod +x "$work/mismatched-compliance-bin/cmp"
PATH="$work/mismatched-compliance-bin:$PATH" expect_failure \
  'generated compliance equality' \
  'archive compliance payload differs from generated THIRD_PARTY_NOTICES' \
  "$source_dist"

cp -R "$source_dist" "$work/dist-invalid-metadata"
jq 'map(if .type == "Archive" and .target == "linux_amd64_v1" then .target = "linux_amd64_v3" else . end)' \
  "$work/dist-invalid-metadata/artifacts.json" >"$work/artifacts.json"
mv "$work/artifacts.json" "$work/dist-invalid-metadata/artifacts.json"
expect_failure 'artifact metadata correlation' 'archive targets do not match the supported release matrix' "$work/dist-invalid-metadata"

mkdir "$work/mismatched-reference-bin"
real_cmp="$(command -v cmp)"
cat >"$work/mismatched-reference-bin/cmp" <<EOF
#!/usr/bin/env bash
if [[ "\${*: -1}" == */reference-* ]]; then
  exit 1
fi
exec "$real_cmp" "\$@"
EOF
chmod +x "$work/mismatched-reference-bin/cmp"
PATH="$work/mismatched-reference-bin:$PATH" expect_failure \
  'target binary reference equality' \
  'archive binary does not match reproducible reference build' \
  "$source_dist"

bash "$verifier" "$source_dist"
