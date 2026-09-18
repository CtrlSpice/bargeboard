#!/usr/bin/env bash
set -euo pipefail

readonly document="${1:?usage: validate-release-sbom.sh DOCUMENT ARCHIVE NAMESPACE CREATED ARCHIVE_VERSION GO_VERSION SOURCE}"
readonly archive="${2:?usage: validate-release-sbom.sh DOCUMENT ARCHIVE NAMESPACE CREATED ARCHIVE_VERSION GO_VERSION SOURCE}"
readonly namespace="${3:?usage: validate-release-sbom.sh DOCUMENT ARCHIVE NAMESPACE CREATED ARCHIVE_VERSION GO_VERSION SOURCE}"
readonly created="${4:?usage: validate-release-sbom.sh DOCUMENT ARCHIVE NAMESPACE CREATED ARCHIVE_VERSION GO_VERSION SOURCE}"
readonly archive_version="${5:?usage: validate-release-sbom.sh DOCUMENT ARCHIVE NAMESPACE CREATED ARCHIVE_VERSION GO_VERSION SOURCE}"
readonly go_version="${6:?usage: validate-release-sbom.sh DOCUMENT ARCHIVE NAMESPACE CREATED ARCHIVE_VERSION GO_VERSION SOURCE}"
readonly source="${7:?usage: validate-release-sbom.sh DOCUMENT ARCHIVE NAMESPACE CREATED ARCHIVE_VERSION GO_VERSION SOURCE}"
readonly project_package=github.com/CtrlSpice/bargeboard
readonly other_relationship_comment="evident-by: indicates the package's existence is evident by the given file"

if ! jq --exit-status --slurp \
  --arg name "$archive" \
  --arg namespace "$namespace" \
  --arg created "$created" \
  --arg package "$project_package" \
  --arg archive_version "$archive_version" \
  --arg go_version "$go_version" \
  --arg other_relationship_comment "$other_relationship_comment" \
  --arg source "$source" '
  length == 1 and (.[0] |
  (keys | sort) == [
    "SPDXID", "creationInfo", "dataLicense", "documentNamespace", "files",
    "name", "packages", "relationships", "spdxVersion"
  ] and
  (.creationInfo | keys | sort) == ["created", "creators", "licenseListVersion"] and
  all(.packages[];
    ([
      "SPDXID", "copyrightText", "downloadLocation", "filesAnalyzed",
      "licenseConcluded", "licenseDeclared", "name", "supplier", "versionInfo"
    ] - keys | length) == 0 and
    (keys - [
      "SPDXID", "checksums", "copyrightText", "downloadLocation", "externalRefs",
      "filesAnalyzed", "licenseConcluded", "licenseDeclared", "name",
      "primaryPackagePurpose", "sourceInfo", "supplier", "versionInfo"
    ] | length) == 0 and
    ((has("primaryPackagePurpose") | not) or .primaryPackagePurpose == "ARCHIVE") and
    ((has("sourceInfo") | not) or (.sourceInfo | type) == "string") and
    ((has("checksums") | not) or (
      (.checksums | type) == "array" and
      all(.checksums[];
        (keys | sort) == ["algorithm", "checksumValue"] and
        .algorithm == "SHA256" and
        (.checksumValue | test("\\A[0-9a-f]{64}\\z"))
      )
    )) and
    ((has("externalRefs") | not) or (
      (.externalRefs | type) == "array" and
      all(.externalRefs[];
        (keys | sort) == ["referenceCategory", "referenceLocator", "referenceType"] and
        .referenceCategory == "PACKAGE-MANAGER" and
        .referenceType == "purl" and
        (.referenceLocator | type) == "string" and (.referenceLocator | length) > 0
      )
    ))
  ) and
  all(.files[];
    (keys | sort) == [
      "SPDXID", "checksums", "copyrightText", "fileName", "fileTypes",
      "licenseConcluded", "licenseInfoInFiles"
    ] and
    (.SPDXID | type) == "string" and
    (.SPDXID | test("\\ASPDXRef-[A-Za-z0-9.-]+\\z")) and
    all(.checksums[];
      (keys | sort) == ["algorithm", "checksumValue"] and
      (.algorithm == "SHA1" or .algorithm == "SHA256") and
      (.checksumValue | test("\\A[0-9a-f]+\\z"))
    )
  ) and
  all(.relationships[];
    if .relationshipType == "OTHER" then
      (keys | sort) == ["comment", "relatedSpdxElement", "relationshipType", "spdxElementId"] and
      .comment == $other_relationship_comment
    else
      (keys | sort) == ["relatedSpdxElement", "relationshipType", "spdxElementId"]
    end
  ) and
  .spdxVersion == "SPDX-2.3" and
  .SPDXID == "SPDXRef-DOCUMENT" and
  .dataLicense == "CC0-1.0" and
  .name == $name and
  .documentNamespace == $namespace and
  .creationInfo.created == $created and
  .creationInfo.licenseListVersion == "3.28" and
  .creationInfo.creators == [
    "Organization: Anchore, Inc",
    "Tool: syft-1.51.1"
  ] and
  (.packages | type) == "array" and
  (.files | type) == "array" and
  (.files | length) == 1 and
  (.relationships | type) == "array" and
  all(.packages[];
    (.name | type) == "string" and (.name | length) > 0 and
    (.SPDXID | type) == "string" and
    (.SPDXID | test("\\ASPDXRef-[A-Za-z0-9.-]+\\z")) and
    (.versionInfo | type) == "string" and (.versionInfo | length) > 0 and
    .supplier == "NOASSERTION" and
    .downloadLocation == "NOASSERTION" and
    .filesAnalyzed == false and
    (.licenseConcluded | type) == "string" and (.licenseConcluded | length) > 0 and
    (.licenseDeclared | type) == "string" and (.licenseDeclared | length) > 0 and
    .copyrightText == "NOASSERTION"
  ) and
  ([.packages[] | select(
    .name == $package and
    (keys | sort) == [
      "SPDXID", "copyrightText", "downloadLocation", "externalRefs",
      "filesAnalyzed", "licenseConcluded", "licenseDeclared", "name",
      "sourceInfo", "supplier", "versionInfo"
    ] and
    .licenseConcluded == "Apache-2.0" and
    .licenseDeclared == "Apache-2.0"
  )] | length) == 1 and
  ([.packages[] | select(
    .name == $name and
    (keys | sort) == [
      "SPDXID", "checksums", "copyrightText", "downloadLocation",
      "filesAnalyzed", "licenseConcluded", "licenseDeclared", "name",
      "primaryPackagePurpose", "supplier", "versionInfo"
    ] and
    .versionInfo == $archive_version and
    .primaryPackagePurpose == "ARCHIVE" and
    .checksums == [{algorithm: "SHA256", checksumValue: ($archive_version | ltrimstr("sha256:"))}] and
    .licenseConcluded == "NOASSERTION" and
    .licenseDeclared == "NOASSERTION"
  )] | length) == 1 and
  ([.packages[] | select(
    .name == "stdlib" and
    (keys | sort) == [
      "SPDXID", "copyrightText", "downloadLocation", "filesAnalyzed",
      "licenseConcluded", "licenseDeclared", "name", "sourceInfo", "supplier",
      "versionInfo"
    ] and
    .versionInfo == $go_version and
    .checksums == null and
    .sourceInfo == ("acquired package info from go module information: " + $source) and
    .licenseConcluded == "NOASSERTION" and
    .licenseDeclared == "BSD-3-Clause"
  )] | length) == 1 and
  (["SPDXRef-DOCUMENT"] + [.packages[].SPDXID] + [.files[].SPDXID]) as $ids |
  ($ids | length) == ($ids | unique | length) and
  all(.relationships[];
    (.spdxElementId as $from | $ids | index($from)) != null and
    (.relatedSpdxElement as $to | $ids | index($to)) != null
  )
  )
' "$document" >/dev/null; then
  printf 'invalid release SPDX document: %s\n' "$document" >&2
  exit 1
fi

if [[ -n "${GITHUB_WORKSPACE:-}" ]] && ! jq --exit-status --slurp \
  --arg workspace "$GITHUB_WORKSPACE" '
  length == 1 and (.[0] | all(.. | strings; contains($workspace) | not))
' "$document" >/dev/null; then
  printf 'SBOM leaks the runner workspace path: %s\n' "$document" >&2
  exit 1
fi
