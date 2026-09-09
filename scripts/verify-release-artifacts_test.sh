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

bash "$verifier" "$source_dist"

cp -R "$source_dist" "$work/dist-extra-file"
unexpected="$work/dist-extra-file/unexpected.tar.gz"
touch "$unexpected"
expect_failure 'root file cardinality' 'release output contains unexpected or missing root files' "$work/dist-extra-file"

cp -R "$source_dist" "$work/dist-extra-subject"
printf '%064d  unexpected.tar.gz\n' 0 >>"$work/dist-extra-subject/checksums.txt"
expect_failure 'checksum subject cardinality' 'checksums.txt does not contain exactly' "$work/dist-extra-subject"

cp -R "$source_dist" "$work/dist-invalid-sbom"
sboms=("$work/dist-invalid-sbom"/*.sbom.spdx.json)
sbom="${sboms[0]}"
jq '.spdxVersion = "SPDX-0.0"' "$sbom" >"$work/invalid-sbom.json"
mv "$work/invalid-sbom.json" "$sbom"
sbom_name="$(basename "$sbom")"
readonly sbom_name
if command -v sha256sum >/dev/null 2>&1; then
  sbom_digest="$(sha256sum "$sbom" | cut -d ' ' -f 1)"
else
  sbom_digest="$(shasum -a 256 "$sbom" | cut -d ' ' -f 1)"
fi
readonly sbom_digest
awk -v name="$sbom_name" -v digest="$sbom_digest" '
  $2 == name { print digest "  " name; next }
  { print }
' "$work/dist-invalid-sbom/checksums.txt" >"$work/checksums.txt"
mv "$work/checksums.txt" "$work/dist-invalid-sbom/checksums.txt"
expect_failure 'SPDX structure' 'invalid release SPDX document' "$work/dist-invalid-sbom"

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
