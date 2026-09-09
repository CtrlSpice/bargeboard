#!/usr/bin/env bash
set -euo pipefail

readonly dist="${1:-build/release}"
readonly source_root="${2:-.}"
readonly metadata="$dist/metadata.json"
readonly artifacts="$dist/artifacts.json"
readonly checksums="$dist/checksums.txt"
readonly project_package=github.com/CtrlSpice/bargeboard
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
    --arg package "$project_package" '
    .spdxVersion == "SPDX-2.3" and
    .dataLicense == "CC0-1.0" and
    .name == $name and
    .documentNamespace == $namespace and
    .creationInfo.created == $created and
    (.packages | type) == "array" and
    (.packages | length) > 0 and
    ([.packages[] | select(
      .name == $package and
      .licenseConcluded == "Apache-2.0" and
      .licenseDeclared == "Apache-2.0"
    )] | length) == 1 and
    any(.packages[]; .name == "google.golang.org/grpc") and
    (.files | type) == "array"
  ' "$sbom" >/dev/null; then
    printf 'invalid release SPDX document: %s\n' "$sbom" >&2
    exit 1
  fi
  if [[ -n "${GITHUB_WORKSPACE:-}" ]] && grep -F "$GITHUB_WORKSPACE" "$sbom" >/dev/null; then
    printf 'SBOM leaks the runner workspace path: %s\n' "$sbom" >&2
    exit 1
  fi

  if [[ "$archive" == *.zip ]]; then
    root="${archive%.zip}"
    binary=bargeboard.exe
    actual_entries="$(unzip -Z1 "$dist/$archive" | LC_ALL=C sort)"
    unzip -q "$dist/$archive" -d "$payload_dir"
  else
    root="${archive%.tar.gz}"
    binary=bargeboard
    actual_entries="$(tar -tzf "$dist/$archive" | LC_ALL=C sort)"
    tar -xzf "$dist/$archive" -C "$payload_dir"
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
    "$expected_vcs_modified"; then
    printf 'archive binary metadata does not match release policy: %s\n' "$archive" >&2
    exit 1
  fi
  if ! LC_ALL=C strings "$binary_path" | LC_ALL=C grep -Fx -- "$version" >/dev/null; then
    printf 'archive binary does not contain release version: %s\n' "$archive" >&2
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
