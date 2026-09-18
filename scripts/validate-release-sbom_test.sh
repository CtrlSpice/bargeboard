#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
readonly validator="$script_dir/validate-release-sbom.sh"
readonly archive=bargeboard_1.2.3_linux_amd64.tar.gz
readonly archive_digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
readonly namespace="https://github.com/CtrlSpice/bargeboard/sbom/sha256-$archive_digest"
readonly created=2026-09-09T00:00:00Z
readonly archive_version="sha256:$archive_digest"
readonly go_version=go1.26.8
readonly source=bargeboard_1.2.3_linux_amd64/bargeboard
readonly project_package=github.com/CtrlSpice/bargeboard
readonly other_relationship_comment="evident-by: indicates the package's existence is evident by the given file"
work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT

fixture() {
  jq -cn \
    --arg archive "$archive" \
    --arg archive_digest "$archive_digest" \
    --arg namespace "$namespace" \
    --arg created "$created" \
    --arg archive_version "$archive_version" \
    --arg go_version "$go_version" \
    --arg source "$source" \
    --arg project "$project_package" \
    --arg other_relationship_comment "$other_relationship_comment" '
    {
      SPDXID: "SPDXRef-DOCUMENT",
      creationInfo: {
        created: $created,
        creators: ["Organization: Anchore, Inc", "Tool: syft-1.51.1"],
        licenseListVersion: "3.28"
      },
      dataLicense: "CC0-1.0",
      documentNamespace: $namespace,
      files: [{
        SPDXID: "SPDXRef-File-bargeboard",
        checksums: [
          {algorithm: "SHA1", checksumValue: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
          {algorithm: "SHA256", checksumValue: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
        ],
        copyrightText: "NOASSERTION",
        fileName: $source,
        fileTypes: ["APPLICATION", "BINARY"],
        licenseConcluded: "NOASSERTION",
        licenseInfoInFiles: ["NOASSERTION"]
      }],
      name: $archive,
      packages: [
        {
          SPDXID: "SPDXRef-Package-archive",
          checksums: [{algorithm: "SHA256", checksumValue: $archive_digest}],
          copyrightText: "NOASSERTION",
          downloadLocation: "NOASSERTION",
          filesAnalyzed: false,
          licenseConcluded: "NOASSERTION",
          licenseDeclared: "NOASSERTION",
          name: $archive,
          primaryPackagePurpose: "ARCHIVE",
          supplier: "NOASSERTION",
          versionInfo: $archive_version
        },
        {
          SPDXID: "SPDXRef-Package-project",
          copyrightText: "NOASSERTION",
          downloadLocation: "NOASSERTION",
          externalRefs: [{
            referenceCategory: "PACKAGE-MANAGER",
            referenceLocator: "pkg:golang/github.com/ctrlspice/bargeboard@v1.2.3",
            referenceType: "purl"
          }],
          filesAnalyzed: false,
          licenseConcluded: "Apache-2.0",
          licenseDeclared: "Apache-2.0",
          name: $project,
          sourceInfo: ("acquired package info from go module information: " + $source),
          supplier: "NOASSERTION",
          versionInfo: "v1.2.3"
        },
        {
          SPDXID: "SPDXRef-Package-stdlib",
          copyrightText: "NOASSERTION",
          downloadLocation: "NOASSERTION",
          filesAnalyzed: false,
          licenseConcluded: "NOASSERTION",
          licenseDeclared: "BSD-3-Clause",
          name: "stdlib",
          sourceInfo: ("acquired package info from go module information: " + $source),
          supplier: "NOASSERTION",
          versionInfo: $go_version
        },
        {
          SPDXID: "SPDXRef-Package-dependency",
          checksums: [{
            algorithm: "SHA256",
            checksumValue: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
          }],
          copyrightText: "NOASSERTION",
          downloadLocation: "NOASSERTION",
          externalRefs: [{
            referenceCategory: "PACKAGE-MANAGER",
            referenceLocator: "pkg:golang/example.com/dependency@v1.0.0",
            referenceType: "purl"
          }],
          filesAnalyzed: false,
          licenseConcluded: "NOASSERTION",
          licenseDeclared: "NOASSERTION",
          name: "example.com/dependency",
          sourceInfo: ("acquired package info from go module information: " + $source),
          supplier: "NOASSERTION",
          versionInfo: "v1.0.0"
        }
      ],
      relationships: [
        {
          spdxElementId: "SPDXRef-DOCUMENT",
          relationshipType: "DESCRIBES",
          relatedSpdxElement: "SPDXRef-Package-archive"
        },
        {
          spdxElementId: "SPDXRef-Package-archive",
          relationshipType: "CONTAINS",
          relatedSpdxElement: "SPDXRef-Package-project"
        },
        {
          spdxElementId: "SPDXRef-Package-dependency",
          relationshipType: "DEPENDENCY_OF",
          relatedSpdxElement: "SPDXRef-Package-project"
        },
        {
          spdxElementId: "SPDXRef-Package-dependency",
          relationshipType: "OTHER",
          relatedSpdxElement: "SPDXRef-File-bargeboard",
          comment: $other_relationship_comment
        }
      ],
      spdxVersion: "SPDX-2.3"
    }
  '
}

validate() {
  local document="${1:?document required}"
  bash "$validator" \
    "$document" \
    "$archive" \
    "$namespace" \
    "$created" \
    "$archive_version" \
    "$go_version" \
    "$source"
}

insert_duplicate_workspace() {
  local input="${1:?input required}"
  local output="${2:?output required}"
  local workspace="${3:?workspace required}"
  local placement="${4:-value}"
  jq --compact-output --ascii-output . "$input" |
    jq --raw-input --slurp --raw-output \
      --arg workspace "$workspace" \
      --arg placement "$placement" '
      "\"supplier\":\"NOASSERTION\"" as $needle |
      if contains($needle) then
        ($workspace | tojson | gsub("/"; "\\u002f")) as $encoded_workspace |
        if $placement == "value" then
          sub($needle; "\"supplier\":" + $encoded_workspace + "," + $needle)
        elif $placement == "key" then
          sub($needle;
            "\"supplier\":{" + $encoded_workspace + ":null}," + $needle
          )
        else
          error("unknown duplicate workspace placement")
        end
      else
        error("duplicate workspace fixture has no insertion point")
      end
    ' >"$output"
}

expect_rejection() {
  local description="${1:?description required}"
  local document="${2:?document required}"
  local expected="${3:?expected output required}"
  local output
  if output="$(validate "$document" 2>&1)"; then
    printf 'expected invalid supplied SPDX evidence: %s\n' "$description" >&2
    exit 1
  fi
  if [[ "$output" != "$expected" ]]; then
    printf 'unexpected SPDX rejection for %s: %s\n' "$description" "$output" >&2
    exit 1
  fi
}

expect_rejection_containing() {
  local description="${1:?description required}"
  local document="${2:?document required}"
  local expected="${3:?expected output required}"
  local output
  if output="$(validate "$document" 2>&1)"; then
    printf 'expected invalid supplied SPDX evidence: %s\n' "$description" >&2
    exit 1
  fi
  if [[ "$output" != *"$expected"* ]]; then
    printf 'unexpected SPDX rejection for %s: %s\n' "$description" "$output" >&2
    exit 1
  fi
}

reject() {
  local description="${1:?description required}"
  local filter="${2:?jq filter required}"
  local document="$work/rejected.json"
  fixture | jq "$filter" >"$document"
  expect_rejection "$description" "$document" "invalid release SPDX document: $document"
}

fixture >"$work/accepted.json"
validate "$work/accepted.json"

{
  printf '{}\n'
  fixture
} >"$work/prefixed-document.json"
expect_rejection \
  'invalid JSON value before the release document' \
  "$work/prefixed-document.json" \
  "invalid release SPDX document: $work/prefixed-document.json"

{
  fixture
  fixture
} >"$work/duplicated-document.json"
expect_rejection \
  'multiple valid release documents' \
  "$work/duplicated-document.json" \
  "invalid release SPDX document: $work/duplicated-document.json"

{
  fixture
  fixture
  printf '{"unterminated"\n'
} >"$work/bounded-document-lookahead.json"
expect_rejection \
  'second document rejects before a malformed third document is parsed' \
  "$work/bounded-document-lookahead.json" \
  "invalid release SPDX document: $work/bounded-document-lookahead.json"

{
  fixture
  printf '{"unterminated"\n'
} >"$work/malformed-second-document.json"
expect_rejection_containing \
  'malformed second document' \
  "$work/malformed-second-document.json" \
  "invalid release SPDX document: $work/malformed-second-document.json"

reject 'unknown top-level field' '.unexpected = true'
reject 'SPDX version' '.spdxVersion = "SPDX-2.2"'
reject 'document SPDX ID' '.SPDXID = "SPDXRef-Other"'
reject 'data license' '.dataLicense = "MIT"'
reject 'document name' '.name = "other.tar.gz"'
reject 'document namespace' '.documentNamespace = "https://example.invalid/sbom"'
reject 'creation time' '.creationInfo.created = "2099-01-01T00:00:00Z"'
reject 'creator identity' '.creationInfo.creators[1] = "Tool: unrelated"'
reject 'license-list version' '.creationInfo.licenseListVersion = "0.0"'
reject 'unknown creation-info field' '.creationInfo.unexpected = true'
reject 'unknown package field' \
  '(.packages[] | select(.name == "example.com/dependency") | .unexpected) = true'
reject 'package SPDX ID grammar' '
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) as $original |
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) = "SPDXRef-invalid id" |
  .relationships |= map(
    if .spdxElementId == $original then .spdxElementId = "SPDXRef-invalid id"
    elif .relatedSpdxElement == $original then .relatedSpdxElement = "SPDXRef-invalid id"
    else . end
  )
'
reject 'package SPDX ID terminal newline' '
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) as $original |
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) = ($original + "\n") |
  .relationships |= map(
    if .spdxElementId == $original then .spdxElementId = ($original + "\n")
    elif .relatedSpdxElement == $original then .relatedSpdxElement = ($original + "\n")
    else . end
  )
'
reject 'package SPDX ID leading line' '
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) as $original |
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) = ("ignored\n" + $original) |
  .relationships |= map(
    if .spdxElementId == $original then .spdxElementId = ("ignored\n" + $original)
    elif .relatedSpdxElement == $original then .relatedSpdxElement = ("ignored\n" + $original)
    else . end
  )
'
reject 'package name' \
  '(.packages[] | select(.name == "example.com/dependency") | .name) = ""'
reject 'package version' \
  '(.packages[] | select(.name == "example.com/dependency") | .versionInfo) = ""'
reject 'package supplier' \
  '(.packages[] | select(.name == "example.com/dependency") | .supplier) = "Organization: Other"'
reject 'package download location' \
  '(.packages[] | select(.name == "example.com/dependency") | .downloadLocation) = "https://example.invalid"'
reject 'package file analysis' \
  '(.packages[] | select(.name == "example.com/dependency") | .filesAnalyzed) = true'
reject 'package copyright' \
  '(.packages[] | select(.name == "example.com/dependency") | .copyrightText) = "Copyright"'
reject 'package checksum shape' \
  '(.packages[] | select(.name == "example.com/dependency") | .checksums[0].algorithm) = "SHA1"'
reject 'package checksum terminal newline' \
  '(.packages[] | select(.name == "example.com/dependency") | .checksums[0].checksumValue) += "\n"'
reject 'package checksum leading line' \
  '(.packages[] | select(.name == "example.com/dependency") | .checksums[0].checksumValue) |= ("ignored\n" + .)'
reject 'package external-reference shape' \
  '(.packages[] | select(.name == "example.com/dependency") | .externalRefs[0].referenceCategory) = "SECURITY"'
reject 'project concluded license' \
  '(.packages[] | select(.name == "github.com/CtrlSpice/bargeboard") | .licenseConcluded) = "NOASSERTION"'
reject 'project declared license' \
  '(.packages[] | select(.name == "github.com/CtrlSpice/bargeboard") | .licenseDeclared) = "NOASSERTION"'
reject 'project package shape' '
  (.packages[] | select(.name == "github.com/CtrlSpice/bargeboard") | .checksums) = [{
    algorithm: "SHA256",
    checksumValue: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
  }]
'
reject 'archive checksum' \
  '(.packages[] | select(.primaryPackagePurpose == "ARCHIVE") | .checksums[0].checksumValue) = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"'
reject 'archive purpose' \
  '(.packages[] | select(.primaryPackagePurpose == "ARCHIVE") | .primaryPackagePurpose) = "SOURCE"'
reject 'archive concluded license' \
  '(.packages[] | select(.primaryPackagePurpose == "ARCHIVE") | .licenseConcluded) = "MIT"'
reject 'archive declared license' \
  '(.packages[] | select(.primaryPackagePurpose == "ARCHIVE") | .licenseDeclared) = "MIT"'
reject 'archive package shape' '
  (.packages[] | select(.primaryPackagePurpose == "ARCHIVE") | .sourceInfo) = "extra source"
'
reject 'standard-library concluded license' \
  '(.packages[] | select(.name == "stdlib") | .licenseConcluded) = "BSD-3-Clause"'
reject 'standard-library declared license' \
  '(.packages[] | select(.name == "stdlib") | .licenseDeclared) = "NOASSERTION"'
reject 'standard-library version' \
  '(.packages[] | select(.name == "stdlib") | .versionInfo) = "go1.99.0"'
reject 'standard-library source' \
  '(.packages[] | select(.name == "stdlib") | .sourceInfo) = "unrelated source"'
reject 'standard-library package shape' '
  (.packages[] | select(.name == "stdlib") | .externalRefs) = [{
    referenceCategory: "PACKAGE-MANAGER",
    referenceLocator: "pkg:golang/stdlib@go1.26.8",
    referenceType: "purl"
  }]
'
reject 'file cardinality' \
  '.files += [(.files[0] | .SPDXID = "SPDXRef-File-other")]'
reject 'unknown file field' '.files[0].unexpected = true'
reject 'file SPDX ID grammar' '
  .files[0].SPDXID as $original |
  .files[0].SPDXID = "SPDXRef-invalid id" |
  .relationships |= map(
    if .spdxElementId == $original then .spdxElementId = "SPDXRef-invalid id"
    elif .relatedSpdxElement == $original then .relatedSpdxElement = "SPDXRef-invalid id"
    else . end
  )
'
reject 'file SPDX ID terminal newline' '
  .files[0].SPDXID as $original |
  .files[0].SPDXID = ($original + "\n") |
  .relationships |= map(
    if .spdxElementId == $original then .spdxElementId = ($original + "\n")
    elif .relatedSpdxElement == $original then .relatedSpdxElement = ($original + "\n")
    else . end
  )
'
reject 'file SPDX ID leading line' '
  .files[0].SPDXID as $original |
  .files[0].SPDXID = ("ignored\n" + $original) |
  .relationships |= map(
    if .spdxElementId == $original then .spdxElementId = ("ignored\n" + $original)
    elif .relatedSpdxElement == $original then .relatedSpdxElement = ("ignored\n" + $original)
    else . end
  )
'
reject 'file checksum shape' '.files[0].checksums[0].algorithm = "MD5"'
reject 'file checksum terminal newline' '.files[0].checksums[0].checksumValue += "\n"'
reject 'file checksum leading line' '.files[0].checksums[0].checksumValue |= ("ignored\n" + .)'
reject 'duplicate valid SPDX ID' '
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) as $dependency |
  (.packages[] | select(.name == "example.com/dependency") | .SPDXID) = "SPDXRef-Package-stdlib" |
  .relationships |= map(
    if .spdxElementId == $dependency then .spdxElementId = "SPDXRef-Package-stdlib"
    elif .relatedSpdxElement == $dependency then .relatedSpdxElement = "SPDXRef-Package-stdlib"
    else . end
  )
'
reject 'cross-category duplicate SPDX ID' '
  .files[0].SPDXID = "SPDXRef-Package-dependency" |
  (.relationships[] | select(.relationshipType == "OTHER") | .relatedSpdxElement) =
    "SPDXRef-Package-dependency"
'
reject 'dangling relationship' '.relationships[0].relatedSpdxElement = "SPDXRef-Missing"'
reject 'ordinary relationship shape' '.relationships[0].comment = "unexpected"'
reject 'OTHER relationship meaning' '
  (.relationships[] | select(.relationshipType == "OTHER") | .comment) = "unverified claim"
'

workspace="$work/workspace"
fixture | jq --arg workspace "$workspace" \
  '(.packages[] | select(.name == "example.com/dependency") | .sourceInfo) = $workspace' \
  >"$work/workspace.json"
GITHUB_WORKSPACE="$workspace" expect_rejection \
  'runner workspace path' \
  "$work/workspace.json" \
  "SBOM leaks the runner workspace path: $work/workspace.json"

workspace="$work/w"$'\303\266'"rkspace"
fixture | jq --ascii-output --arg workspace "$workspace" \
  '(.packages[] | select(.name == "example.com/dependency") | .sourceInfo) = $workspace' \
  >"$work/escaped-workspace.json"
if grep -F "$workspace" "$work/escaped-workspace.json" >/dev/null; then
  printf 'escaped workspace fixture contains the decoded path\n' >&2
  exit 1
fi
GITHUB_WORKSPACE="$workspace" expect_rejection \
  'JSON-escaped runner workspace path' \
  "$work/escaped-workspace.json" \
  "SBOM leaks the runner workspace path: $work/escaped-workspace.json"

duplicate_workspace=/runner/workspace
insert_duplicate_workspace \
  "$work/accepted.json" \
  "$work/duplicate-workspace.json" \
  "$duplicate_workspace"
if grep -F "$duplicate_workspace" "$work/duplicate-workspace.json" >/dev/null; then
  printf 'duplicate workspace fixture contains the decoded path\n' >&2
  exit 1
fi
GITHUB_WORKSPACE="$duplicate_workspace" expect_rejection \
  'JSON-escaped runner workspace path in an overwritten duplicate member' \
  "$work/duplicate-workspace.json" \
  "SBOM leaks the runner workspace path: $work/duplicate-workspace.json"

insert_duplicate_workspace \
  "$work/accepted.json" \
  "$work/duplicate-workspace-key.json" \
  "$duplicate_workspace" \
  key
if grep -F "$duplicate_workspace" "$work/duplicate-workspace-key.json" >/dev/null; then
  printf 'duplicate workspace-key fixture contains the decoded path\n' >&2
  exit 1
fi
GITHUB_WORKSPACE="$duplicate_workspace" expect_rejection \
  'JSON-escaped runner workspace path in an overwritten duplicate object key' \
  "$work/duplicate-workspace-key.json" \
  "SBOM leaks the runner workspace path: $work/duplicate-workspace-key.json"

real_jq="$(command -v jq)"
mkdir "$work/bin"
cat >"$work/bin/jq" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  if [[ "$argument" == --stream ]]; then
    exit 5
  fi
done
exec "${REAL_JQ:?real jq required}" "$@"
EOF
chmod +x "$work/bin/jq"
if output="$(
  PATH="$work/bin:$PATH" \
    REAL_JQ="$real_jq" \
    GITHUB_WORKSPACE=/runner/workspace \
    validate "$work/accepted.json" 2>&1
)"; then
  printf 'expected workspace scanner failure to reject supplied SPDX evidence\n' >&2
  exit 1
fi
if [[ "$output" != "invalid release SPDX document: $work/accepted.json" ]]; then
  printf 'unexpected workspace-scanner rejection: %s\n' "$output" >&2
  exit 1
fi
