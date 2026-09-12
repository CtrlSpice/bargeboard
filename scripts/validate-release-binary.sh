#!/usr/bin/env bash
set -euo pipefail

readonly expected_go_version="${1:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
readonly expected_goos="${2:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
readonly expected_goarch="${3:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
readonly expected_tuning_key="${4:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
readonly expected_tuning="${5:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
readonly expected_commit="${6:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
readonly expected_modified="${7-}"
readonly expected_vcs_time="${8:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
readonly expected_module_version="${9:?usage: validate-release-binary.sh GO_VERSION GOOS GOARCH TUNING_KEY TUNING COMMIT MODIFIED VCS_TIME MODULE_VERSION}"
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
  --arg modified "$expected_modified" \
  --arg vcs_time "$expected_vcs_time" \
  --arg module_version "$expected_module_version" '
    (.Settings | map({(.Key): .Value}) | add) as $settings |
    .GoVersion == $go_version and
    .Path == $package and
    .Main.Path == $package and
    .Main.Version == $module_version and
    (.Deps | type) == "array" and
    $settings["-buildmode"] == "exe" and
    $settings["-trimpath"] == "true" and
    $settings.CGO_ENABLED == "0" and
    $settings.GOOS == $goos and
    $settings.GOARCH == $goarch and
    $settings[$tuning_key] == $tuning and
    $settings.vcs == "git" and
    $settings["vcs.revision"] == $commit and
    $settings["vcs.time"] == $vcs_time and
    ($settings["vcs.modified"] == "true" or $settings["vcs.modified"] == "false") and
    ($modified == "" or $settings["vcs.modified"] == $modified)
  ' <<<"$payload" >/dev/null; then
  printf 'release binary metadata does not match policy\n' >&2
  exit 1
fi
