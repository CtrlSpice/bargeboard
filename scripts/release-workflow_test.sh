#!/usr/bin/env bash
set -euo pipefail

readonly workflow=.github/workflows/release.yml
readonly goreleaser_config=.goreleaser.yaml

require_count() {
  local expected="${1:?expected count required}"
  local text="${2:?text required}"
  local file="${3:-$workflow}"
  local actual
  actual="$(grep -Fc -- "$text" "$file" || true)"
  if [[ "$actual" != "$expected" ]]; then
    printf '%s contains %s copies of %q; expected %s\n' "$file" "$actual" "$text" "$expected" >&2
    exit 1
  fi
}

require_count 1 '  repository_dispatch:'
require_count 3 '          ref: ${{ github.sha }}'
require_count 1 '          goreleaser release --clean --skip=publish'
require_count 0 '--prepare'
require_count 0 'goreleaser publish'
require_count 1 '          bash scripts/build-release.sh "$RELEASE_TAG"'
require_count 1 '            const { uploadRelease } = require('
require_count 1 '            const releaseID = await uploadRelease({'
require_count 1 '            core.setOutput("release_id", String(releaseID));'
require_count 1 '          RELEASE_ID: ${{ steps.upload_release.outputs.release_id }}'
require_count 1 '              releaseID: Number(process.env.RELEASE_ID),'
require_count 1 '          REFERENCE_DIGESTS: ${{ steps.reference_build.outputs.digests }}'
require_count 1 '          test "$current" = "$REFERENCE_DIGESTS"'
require_count 0 '${{ github.ref_name }}'
require_count 0 '  push:'
require_count 0 '      artifact-metadata: write'
require_count 1 '    needs: [validate, native]'
require_count 1 '    needs: [version, release_build]' .github/workflows/check.yml
for file in "$workflow" .github/workflows/check.yml; do
  actual_matrix="$(awk '
    $1 == "-" && $2 == "runner:" { runner = $3 }
    $1 == "target:" && $2 !~ /^\$\{\{/ { print runner " " $2 }
  ' "$file")"
  if [[ "$actual_matrix" != $'ubuntu-24.04 linux_amd64_v1\nubuntu-24.04-arm linux_arm64_v8.0\nmacos-15 darwin_arm64_v8.0\nwindows-2025 windows_amd64_v1' ]]; then
    printf 'unexpected native runner/target matrix in %s:\n%s\n' "$file" "$actual_matrix" >&2
    exit 1
  fi
  require_count 0 darwin_amd64 "$file"
  require_count 1 '        uses: ./.github/actions/build-native' "$file"
  require_count 1 '        uses: actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333' "$file"
  require_count 1 '          skip-decompress: true' "$file"
  require_count 1 '          digest-mismatch: error' "$file"
  require_count 1 '          bash scripts/verify-native-release-inputs.sh "$native_binaries"' "$file"
  require_count 1 '          NATIVE_BUILD_SET: reference' "$file"
  # Download credentials or repository/run selectors would widen the transfer
  # beyond the current workflow run.
  download="$(sed -n '/      - name: Download native binaries from this run/,/      - name: Verify native inputs/p' "$file")"
  if grep -E '(github-token|repository|run-id|artifact-ids):' <<<"$download"; then
    printf 'native artifact download must use only its current-run token\n' >&2
    exit 1
  fi
  download_line="$(grep -nF '      - name: Verify native inputs' "$file" | cut -d: -f1)"
  package_line="$(grep -nE '      - name: (Build release snapshot|Build independent release subjects)' "$file" | cut -d: -f1)"
  if [[ "$download_line" -ge "$package_line" ]]; then
    printf 'native input verification must precede packaging: %s\n' "$file" >&2
    exit 1
  fi
done
native_action=.github/actions/build-native/action.yml
require_count 2 '      uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9' "$native_action"
require_count 2 '        archive: false' "$native_action"
require_count 2 '        if-no-files-found: error' "$native_action"
require_count 1 '        git clone --no-hardlinks --no-checkout "$GITHUB_WORKSPACE" "$RUNNER_TEMP/native-source"' "$native_action"
require_count 1 '        GOCACHE: ${{ runner.temp }}/native-reference-go-build' "$native_action"
require_count 1 '        GOMODCACHE: ${{ runner.temp }}/native-reference-go-mod' "$native_action"
require_count 1 '      run: cmp "$CANDIDATE_BINARY" "$REFERENCE_BINARY"' "$native_action"
require_count 1 '    builder: prebuilt' "$goreleaser_config"
require_count 0 'CGO_ENABLED=0' "$goreleaser_config"
require_count 0 darwin_amd64 "$goreleaser_config"
if [[ "$(grep -A1 -x 'changelog:' "$goreleaser_config")" != $'changelog:\n  disable: true' ]]; then
  printf 'GoReleaser changelog generation must remain disabled for the closed release output\n' >&2
  exit 1
fi

reproducibility_line="$(grep -nF -- '      - name: Check reproducible release subjects' "$workflow" | cut -d: -f1)"
verify_line="$(grep -nF -- '      - name: Verify release artifacts' "$workflow" | cut -d: -f1)"
ref_line="$(grep -nF -- '      - name: Revalidate release ref before publication' "$workflow" | cut -d: -f1)"
controls_line="$(grep -nF -- '      - name: Revalidate release controls before publication' "$workflow" | cut -d: -f1)"
match_line="$(grep -nF -- '      - name: Match built release ref' "$workflow" | cut -d: -f1)"
upload_line="$(grep -nF -- '      - name: Upload verified draft' "$workflow" | cut -d: -f1)"
attest_line="$(grep -nF -- '      - name: Attest archives and SBOMs' "$workflow" | cut -d: -f1)"
checksum_attest_line="$(grep -nF -- '      - name: Attest checksum manifest' "$workflow" | cut -d: -f1)"
publish_line="$(grep -nF -- '      - name: Publish verified draft' "$workflow" | cut -d: -f1)"
if [[ -z "$reproducibility_line" || -z "$verify_line" || -z "$ref_line" ||
  -z "$controls_line" || -z "$match_line" || -z "$upload_line" || -z "$attest_line" ||
  -z "$checksum_attest_line" || -z "$publish_line" ||
  "$upload_line" -le "$reproducibility_line" || "$upload_line" -le "$verify_line" ||
  "$attest_line" -le "$upload_line" || "$checksum_attest_line" -le "$upload_line" ||
  "$ref_line" -le "$attest_line" || "$ref_line" -le "$checksum_attest_line" ||
  "$controls_line" -le "$attest_line" || "$controls_line" -le "$checksum_attest_line" ||
  "$match_line" -le "$attest_line" || "$match_line" -le "$checksum_attest_line" ||
  "$publish_line" -le "$ref_line" || "$publish_line" -le "$controls_line" ||
  "$publish_line" -le "$match_line" ]]; then
  printf 'release workflow gates, uploads, attests, or publishes out of order\n' >&2
  exit 1
fi
