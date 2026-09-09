#!/usr/bin/env bash
set -euo pipefail

readonly expected_go_version="${1:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED}"
readonly expected_goos="${2:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED}"
readonly expected_goarch="${3:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED}"
readonly expected_tuning_key="${4:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED}"
readonly expected_tuning="${5:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED}"
readonly expected_commit="${6:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED}"
readonly expected_modified="${7-}"
readonly project_package=github.com/CtrlSpice/bargeboard

payload="$(jq -ce .)" || {
  printf 'release binary metadata is not valid JSON\n' >&2
  exit 1
}
readonly payload

if ! jq -e \
  --arg package "$project_package" \
  --arg go_version "$expected_go_version" \
  --arg goos "$expected_goos" \
  --arg goarch "$expected_goarch" \
  --arg tuning_key "$expected_tuning_key" \
  --arg tuning "$expected_tuning" \
  --arg commit "$expected_commit" \
  --arg modified "$expected_modified" '
    (.Settings | map({(.Key): .Value}) | add) as $settings |
    .GoVersion == $go_version and
    .Path == $package and
    .Main.Path == $package and
    $settings["-buildmode"] == "exe" and
    $settings["-trimpath"] == "true" and
    $settings.CGO_ENABLED == "0" and
    $settings.GOOS == $goos and
    $settings.GOARCH == $goarch and
    $settings[$tuning_key] == $tuning and
    $settings["vcs.revision"] == $commit and
    ($modified == "" or $settings["vcs.modified"] == $modified)
  ' <<<"$payload" >/dev/null; then
  printf 'release binary metadata does not match policy\n' >&2
  exit 1
fi
