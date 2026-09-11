#!/usr/bin/env bash
set -euo pipefail

readonly workflow=.github/workflows/release.yml
readonly goreleaser_config=.goreleaser.yaml

require_count() {
  local expected="${1:?expected count required}"
  local text="${2:?text required}"
  local actual
  actual="$(grep -Fc -- "$text" "$workflow" || true)"
  if [[ "$actual" != "$expected" ]]; then
    printf 'release workflow contains %s copies of %q; expected %s\n' "$actual" "$text" "$expected" >&2
    exit 1
  fi
}

require_count 1 '  repository_dispatch:'
require_count 2 '          ref: ${{ github.sha }}'
require_count 1 '          goreleaser release --clean --prepare'
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
