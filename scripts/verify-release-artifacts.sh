#!/usr/bin/env bash
set -euo pipefail

readonly dist="${1:-build/release}"
readonly source_root="${2:-.}"
readonly metadata="$dist/metadata.json"
readonly artifacts="$dist/artifacts.json"
readonly checksums="$dist/checksums.txt"
readonly project_package=github.com/CtrlSpice/bargeboard
readonly other_relationship_comment="evident-by: indicates the package's existence is evident by the given file"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir

for file in "$metadata" "$artifacts" "$checksums"; do
  if [[ ! -f "$file" ]]; then
    printf 'missing release metadata: %s\n' "$file" >&2
    exit 1
  fi
done

version="$(jq -er '.version | select(type == "string" and length > 0)' "$metadata")"
readonly version
if [[ ! "$version" =~ ^[0-9A-Za-z][0-9A-Za-z.+-]*$ || ${#version} -gt 130 ]]; then
  printf 'release metadata contains an unsafe version: %s\n' "$version" >&2
  exit 1
fi
commit_epoch="$(git show -s --format=%ct HEAD)"
readonly commit_epoch
created="$(jq -nr --argjson epoch "$commit_epoch" '$epoch | todateiso8601')"
readonly created
readonly expected_commit="${EXPECTED_COMMIT:-$(git rev-parse --verify 'HEAD^{commit}')}"
readonly expected_go_version="${EXPECTED_GO_VERSION:-$(go env GOVERSION)}"
readonly expected_vcs_modified="${EXPECTED_VCS_MODIFIED:-}"
readonly archives=(
  "bargeboard_${version}_darwin_amd64.tar.gz"
  "bargeboard_${version}_darwin_arm64.tar.gz"
  "bargeboard_${version}_linux_amd64.tar.gz"
  "bargeboard_${version}_linux_arm64.tar.gz"
  "bargeboard_${version}_windows_amd64.zip"
)
expected_subjects=()
for archive in "${archives[@]}"; do
  expected_subjects+=("$archive" "$archive.sbom.spdx.json")
done

for archive in "${archives[@]}"; do
  if [[ "$archive" == *.zip ]]; then
    root="${archive%.zip}"
    binary=bargeboard.exe
  else
    root="${archive%.tar.gz}"
    binary=bargeboard
  fi
  if ! go run "$script_dir/release_archive.go" "$dist/$archive" "$root" "$binary" "$commit_epoch"; then
    printf 'archive metadata does not match release policy: %s\n' "$archive" >&2
    exit 1
  fi
done

checksum_subjects=()
while IFS= read -r line; do
  if [[ ! "$line" =~ ^([0-9a-f]{64})[[:space:]]{2}([^[:space:]]+)$ ]]; then
    printf 'invalid checksum line: %s\n' "$line" >&2
    exit 1
  fi
  checksum_subjects+=("${BASH_REMATCH[2]}")
done <"$checksums"

expected_sorted="$(printf '%s\n' "${expected_subjects[@]}" | LC_ALL=C sort)"
checksum_sorted="$(printf '%s\n' "${checksum_subjects[@]}" | LC_ALL=C sort)"
readonly expected_sorted checksum_sorted
if [[ "$expected_sorted" != "$checksum_sorted" ]]; then
  printf 'checksums.txt does not contain exactly the expected archives and SBOMs\n' >&2
  exit 1
fi

(
  cd "$dist"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum --check --strict checksums.txt
  else
    shasum -a 256 --check checksums.txt
  fi
)

expected_root_files="$(printf '%s\n' \
  artifacts.json \
  checksums.txt \
  config.yaml \
  metadata.json \
  "${expected_subjects[@]}" | LC_ALL=C sort)"
actual_root_files="$(find "$dist" -maxdepth 1 -type f -exec basename {} \; | LC_ALL=C sort)"
readonly expected_root_files actual_root_files
if [[ "$expected_root_files" != "$actual_root_files" ]]; then
  printf 'release output contains unexpected or missing root files\n' >&2
  exit 1
fi

readonly expected_targets=$'darwin_amd64_v1\ndarwin_arm64_v8.0\nlinux_amd64_v1\nlinux_arm64_v8.0\nwindows_amd64_v1'
actual_targets="$(jq -r '.[] | select(.type == "Archive") | .target' "$artifacts" | LC_ALL=C sort)"
readonly actual_targets
if [[ "$actual_targets" != "$expected_targets" ]]; then
  printf 'archive targets do not match the supported release matrix\n' >&2
  exit 1
fi

if ! jq -e '
  length == 17 and
  ([.[] | select(.type == "Archive")] | length == 5) and
  ([.[] | select(.type == "Binary")] | length == 5) and
  ([.[] | select(.type == "Checksum")] | length == 1) and
  ([.[] | select(.type == "Metadata")] | length == 1) and
  ([.[] | select(.type == "SBOM")] | length == 5)
' "$artifacts" >/dev/null; then
  printf 'artifacts.json does not contain the expected release artifacts\n' >&2
  exit 1
fi

payload_dir="$(mktemp -d)"
readonly payload_dir
trap 'rm -rf "$payload_dir"' EXIT
repository_root="$(git rev-parse --show-toplevel)"
readonly repository_root
reference_binary="$payload_dir/reference-bargeboard"
(
  cd "$repository_root"
  CGO_ENABLED=0 go build -buildvcs=true -mod=readonly -trimpath -o "$reference_binary" .
)
expected_module_version="$(go version -m -json "$reference_binary" | jq -er \
  --arg package "$project_package" '
    select(.Path == $package and .Main.Path == $package) |
    .Main.Version | select(type == "string" and length > 0)
  ')"
readonly expected_module_version

for archive in "${archives[@]}"; do
  case "$archive" in
    *_darwin_amd64.tar.gz)
      expected_goos=darwin
      expected_goarch=amd64
      expected_tuning_key=GOAMD64
      expected_tuning=v1
      expected_target=darwin_amd64_v1
      ;;
    *_darwin_arm64.tar.gz)
      expected_goos=darwin
      expected_goarch=arm64
      expected_tuning_key=GOARM64
      expected_tuning=v8.0
      expected_target=darwin_arm64_v8.0
      ;;
    *_linux_amd64.tar.gz)
      expected_goos=linux
      expected_goarch=amd64
      expected_tuning_key=GOAMD64
      expected_tuning=v1
      expected_target=linux_amd64_v1
      ;;
    *_linux_arm64.tar.gz)
      expected_goos=linux
      expected_goarch=arm64
      expected_tuning_key=GOARM64
      expected_tuning=v8.0
      expected_target=linux_arm64_v8.0
      ;;
    *_windows_amd64.zip)
      expected_goos=windows
      expected_goarch=amd64
      expected_tuning_key=GOAMD64
      expected_tuning=v1
      expected_target=windows_amd64_v1
      ;;
    *)
      printf 'unexpected release archive: %s\n' "$archive" >&2
      exit 1
      ;;
  esac
  if [[ "$archive" == *.zip ]]; then
    root="${archive%.zip}"
    binary=bargeboard.exe
  else
    root="${archive%.tar.gz}"
    binary=bargeboard
  fi

  if ! jq -e --arg name "$archive" --arg target "$expected_target" '
    ([.[] | select(
      .type == "Archive" and
      .name == $name and
      .target == $target and
      .extra.ID == "release"
    )] | length) == 1 and
    ([.[] | select(
      .type == "Binary" and
      .target == $target and
      .extra.ID == "bargeboard"
    )] | length) == 1
  ' "$artifacts" >/dev/null; then
    printf 'artifact metadata does not match archive: %s\n' "$archive" >&2
    exit 1
  fi

  sbom="$dist/$archive.sbom.spdx.json"
  if command -v sha256sum >/dev/null 2>&1; then
    archive_digest="$(sha256sum "$dist/$archive" | cut -d ' ' -f 1)"
  else
    archive_digest="$(shasum -a 256 "$dist/$archive" | cut -d ' ' -f 1)"
  fi
  namespace="https://github.com/CtrlSpice/bargeboard/sbom/sha256-$archive_digest"
  if ! jq -e \
    --arg name "$archive" \
    --arg namespace "$namespace" \
    --arg created "$created" \
    --arg package "$project_package" \
    --arg archive_version "sha256:$archive_digest" \
    --arg go_version "$expected_go_version" \
    --arg other_relationship_comment "$other_relationship_comment" \
    --arg source "$root/$binary" '
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
          (.checksumValue | test("^[0-9a-f]{64}$"))
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
      (.SPDXID | test("^SPDXRef-[A-Za-z0-9.-]+$")) and
      all(.checksums[];
        (keys | sort) == ["algorithm", "checksumValue"] and
        (.algorithm == "SHA1" or .algorithm == "SHA256") and
        (.checksumValue | test("^[0-9a-f]+$"))
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
      (.SPDXID | test("^SPDXRef-[A-Za-z0-9.-]+$")) and
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
  ' "$sbom" >/dev/null; then
    printf 'invalid release SPDX document: %s\n' "$sbom" >&2
    exit 1
  fi
  if [[ -n "${GITHUB_WORKSPACE:-}" ]] && grep -F "$GITHUB_WORKSPACE" "$sbom" >/dev/null; then
    printf 'SBOM leaks the runner workspace path: %s\n' "$sbom" >&2
    exit 1
  fi

  if [[ "$archive" == *.zip ]]; then
    actual_entries="$(unzip -Z1 "$dist/$archive" | LC_ALL=C sort)"
  else
    actual_entries="$(tar -tzf "$dist/$archive" | LC_ALL=C sort)"
  fi
  expected_entries="$(printf '%s\n' \
    "$root/LICENSE" \
    "$root/README.md" \
    "$root/config.yaml" \
    "$root/$binary" | LC_ALL=C sort)"
  if [[ "$actual_entries" != "$expected_entries" ]]; then
    printf 'archive has an unexpected payload: %s\n' "$archive" >&2
    exit 1
  fi
  if [[ "$archive" == *.zip ]]; then
    unzip -q "$dist/$archive" -d "$payload_dir"
  else
    tar -xzf "$dist/$archive" -C "$payload_dir"
  fi
  for tracked_file in LICENSE README.md config.yaml; do
    if [[ "$archive" == *.zip ]]; then
      if ! cmp -s "$source_root/$tracked_file" <(unzip -p "$dist/$archive" "$root/$tracked_file"); then
        printf 'archive payload differs from tracked %s: %s\n' "$tracked_file" "$archive" >&2
        exit 1
      fi
    elif ! cmp -s "$source_root/$tracked_file" <(tar -xOzf "$dist/$archive" "$root/$tracked_file"); then
      printf 'archive payload differs from tracked %s: %s\n' "$tracked_file" "$archive" >&2
      exit 1
    fi
  done

  binary_path="$payload_dir/$root/$binary"
  if [[ ! -f "$binary_path" || -L "$binary_path" || ! -x "$binary_path" ]]; then
    printf 'archive binary is not a regular executable: %s\n' "$archive" >&2
    exit 1
  fi
  build_info="$(go version -m -json "$binary_path")"
  if ! printf '%s\n' "$build_info" | bash "$script_dir/validate-release-binary.sh" \
    "$expected_go_version" \
    "$expected_goos" \
    "$expected_goarch" \
    "$expected_tuning_key" \
    "$expected_tuning" \
    "$expected_commit" \
    "$expected_vcs_modified" \
    "$created" \
    "$expected_module_version"; then
    printf 'archive binary metadata does not match release policy: %s\n' "$archive" >&2
    exit 1
  fi

  reference_target_binary="$payload_dir/reference-$expected_target"
  (
    cd "$repository_root"
    env \
      CGO_ENABLED=0 \
      GOOS="$expected_goos" \
      GOARCH="$expected_goarch" \
      "$expected_tuning_key=$expected_tuning" \
      go build \
        -buildvcs=true \
        -mod=readonly \
        -trimpath \
        -ldflags "-s -w -X main.version=$version" \
        -o "$reference_target_binary" \
        .
  )
  if ! cmp -s "$binary_path" "$reference_target_binary"; then
    printf 'archive binary does not match reproducible reference build: %s\n' "$archive" >&2
    exit 1
  fi

  if command -v sha1sum >/dev/null 2>&1; then
    binary_sha1="$(sha1sum "$binary_path" | cut -d ' ' -f 1)"
  else
    binary_sha1="$(shasum -a 1 "$binary_path" | cut -d ' ' -f 1)"
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    binary_sha256="$(sha256sum "$binary_path" | cut -d ' ' -f 1)"
  else
    binary_sha256="$(shasum -a 256 "$binary_path" | cut -d ' ' -f 1)"
  fi
  if ! jq -e \
    --arg file "$root/$binary" \
    --arg sha1 "$binary_sha1" \
    --arg sha256 "$binary_sha256" '
      .files[0].fileName == $file and
      (.files[0].SPDXID | test("^SPDXRef-[A-Za-z0-9.-]+$")) and
      .files[0].fileTypes == ["APPLICATION", "BINARY"] and
      ([.files[0].checksums[] | [.algorithm, .checksumValue]] | sort) == ([
        ["SHA1", $sha1],
        ["SHA256", $sha256]
      ] | sort) and
      .files[0].licenseConcluded == "NOASSERTION" and
      .files[0].licenseInfoInFiles == ["NOASSERTION"] and
      .files[0].copyrightText == "NOASSERTION"
    ' "$sbom" >/dev/null; then
    printf 'SBOM binary file does not match archive payload: %s\n' "$sbom" >&2
    exit 1
  fi

  module_version="$(jq -er --arg package "$project_package" '
    select(.Path == $package and .Main.Path == $package) |
    .Main.Version | select(type == "string" and length > 0)
  ' <<<"$build_info")"
  project_purl="$(jq -nr \
    --arg package "$project_package" \
    --arg version "$module_version" '
      "pkg:golang/" + ($package | ascii_downcase) + "@" + ($version | @uri)
    ')"
  if ! jq -e \
    --arg package "$project_package" \
    --arg version "$module_version" \
    --arg purl "$project_purl" \
    --arg source "$root/$binary" '
      ([.packages[] | select(
        .name == $package and
        .versionInfo == $version and
        .checksums == null and
        .sourceInfo == ("acquired package info from go module information: " + $source) and
        ([.externalRefs[]? | select(.referenceType == "purl")]) == [{
          referenceCategory: "PACKAGE-MANAGER",
          referenceType: "purl",
          referenceLocator: $purl
        }]
      )] | length) == 1
    ' "$sbom" >/dev/null; then
    printf 'SBOM project package does not match binary module identity: %s\n' "$sbom" >&2
    exit 1
  fi

  expected_packages="$(jq -r \
    --arg archive "$archive" \
    --arg archive_version "sha256:$archive_digest" \
    --arg package "$project_package" \
    --arg module_version "$module_version" \
    --arg go_version "$expected_go_version" '
      ([
        [$archive, $archive_version],
        [$package, $module_version],
        ["stdlib", $go_version]
      ] + [.Deps[] | [(.Replace.Path // .Path), (.Replace.Version // .Version // "")]])
      | sort_by(.[0], .[1])
      | .[]
      | @tsv
    ' <<<"$build_info")"
  actual_packages="$(jq -r '
    [.packages[] | [.name, (.versionInfo // "")]]
    | sort_by(.[0], .[1])
    | .[]
    | @tsv
  ' "$sbom")"
  if [[ "$actual_packages" != "$expected_packages" ]]; then
    printf 'SBOM package inventory does not match binary build info: %s\n' "$sbom" >&2
    exit 1
  fi

  while IFS=$'\t' read -r dependency_name dependency_version dependency_sum; do
    if [[ ! "$dependency_sum" =~ ^h1:[A-Za-z0-9+/]{43}=$ ]]; then
      printf 'binary dependency lacks an authenticated Go module sum: %s\n' "$dependency_name" >&2
      exit 1
    fi
    dependency_checksum="$(
      printf '%s' "${dependency_sum#h1:}" |
        openssl base64 -d -A |
        od -An -v -tx1 |
        tr -d ' \n'
    )"
    if [[ ! "$dependency_checksum" =~ ^[0-9a-f]{64}$ ]]; then
      printf 'invalid Go module sum for binary dependency: %s\n' "$dependency_name" >&2
      exit 1
    fi
    dependency_purl="$(jq -nr \
      --arg package "$dependency_name" \
      --arg version "$dependency_version" '
        "pkg:golang/" + ($package | ascii_downcase) + "@" + ($version | @uri)
      ')"
    if ! jq -e \
      --arg package "$dependency_name" \
      --arg version "$dependency_version" \
      --arg checksum "$dependency_checksum" \
      --arg purl "$dependency_purl" \
      --arg source "$root/$binary" '
        ([.packages[] | select(
          .name == $package and
          (keys | sort) == [
            "SPDXID", "checksums", "copyrightText", "downloadLocation",
            "externalRefs", "filesAnalyzed", "licenseConcluded",
            "licenseDeclared", "name", "sourceInfo", "supplier", "versionInfo"
          ] and
          .versionInfo == $version and
          .sourceInfo == ("acquired package info from go module information: " + $source) and
          .checksums == [{algorithm: "SHA256", checksumValue: $checksum}] and
          .licenseConcluded == "NOASSERTION" and
          .licenseDeclared == "NOASSERTION" and
          ([.externalRefs[]? | select(.referenceType == "purl")]) == [{
            referenceCategory: "PACKAGE-MANAGER",
            referenceType: "purl",
            referenceLocator: $purl
          }]
        )] | length) == 1
      ' "$sbom" >/dev/null; then
      printf 'SBOM dependency does not match authenticated Go module metadata: %s\n' "$dependency_name" >&2
      exit 1
    fi
  done < <(jq -r '
    .Deps[] |
    [
      (.Replace.Path // .Path),
      (.Replace.Version // .Version // ""),
      (.Replace.Sum // .Sum // "")
    ] | @tsv
  ' <<<"$build_info")

  if ! jq -e --arg archive "$archive" --arg package "$project_package" '
    ([.packages[] | select(.name == $archive)] | .[0].SPDXID) as $root |
    ([.packages[] | select(.name == $package)] | .[0].SPDXID) as $project |
    .files[0].SPDXID as $binary |
    ([.packages[] | select(.SPDXID != $root) | .SPDXID] | sort) as $contained |
    ([.packages[] | select(.SPDXID != $root and .SPDXID != $project) | .SPDXID] | sort) as $dependencies |
    ([
      ["SPDXRef-DOCUMENT", "DESCRIBES", $root]
    ] +
      ($contained | map([$root, "CONTAINS", .])) +
      ($dependencies | map([., "DEPENDENCY_OF", $project])) +
      ($contained | map([., "OTHER", $binary])) |
      sort
    ) as $expected_relationships |
    ([.relationships[] | [
      .spdxElementId,
      .relationshipType,
      .relatedSpdxElement
    ]] | sort) == $expected_relationships
  ' "$sbom" >/dev/null; then
    printf 'SBOM relationships do not match binary package ownership: %s\n' "$sbom" >&2
    exit 1
  fi
done

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)
    readonly native_archive="bargeboard_${version}_darwin_arm64.tar.gz"
    ;;
  Darwin-x86_64)
    readonly native_archive="bargeboard_${version}_darwin_amd64.tar.gz"
    ;;
  Linux-aarch64)
    readonly native_archive="bargeboard_${version}_linux_arm64.tar.gz"
    ;;
  Linux-x86_64)
    readonly native_archive="bargeboard_${version}_linux_amd64.tar.gz"
    ;;
  *)
    printf 'unsupported verification host: %s %s\n' "$(uname -s)" "$(uname -m)" >&2
    exit 1
    ;;
esac
readonly native_root="${native_archive%.tar.gz}"
actual_version="$("$payload_dir/$native_root/bargeboard" --version)"
readonly actual_version
readonly expected_version="bargeboard version $version"
if [[ "$actual_version" != "$expected_version" ]]; then
  printf 'unexpected binary version: %s\n' "$actual_version" >&2
  exit 1
fi
