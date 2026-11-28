#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 1 ]]; then
  printf 'usage: verify-native-release-inputs.sh DIRECTORY\n' >&2
  exit 1
fi
readonly directory="$1"
if [[ ! -d "$directory" || -L "$directory" ]]; then
  printf 'native input must be a regular directory: %s\n' "$directory" >&2
  exit 1
fi
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
source "$script_dir/build-native-release.sh"
readonly targets=(darwin_arm64_v8.0 linux_amd64_v1 linux_arm64_v8.0 windows_amd64_v1)
expected=()
for target in "${targets[@]}"; do
  extension=''
  if [[ "$target" == windows_* ]]; then
    extension=.exe
  fi
  for kind in candidate reference; do
    expected+=("$directory/bargeboard-native-$kind-$target$extension")
  done
done
shopt -s nullglob dotglob
actual=("$directory"/*)
if [[ "$(printf '%s\n' "${actual[@]}" | LC_ALL=C sort)" != \
  "$(printf '%s\n' "${expected[@]}" | LC_ALL=C sort)" ]]; then
  printf 'native input must contain exactly the four candidate and four reference binaries\n' >&2
  exit 1
fi
for file in "${expected[@]}"; do
  if [[ ! -f "$file" || -L "$file" || ! -s "$file" ]]; then
    printf 'native input is not a nonempty regular file: %s\n' "$file" >&2
    exit 1
  fi
done

commit="${EXPECTED_COMMIT:-$(git rev-parse --verify 'HEAD^{commit}')}"
epoch="$(git show -s --format=%ct "$commit")"
created="$(jq -nr --argjson epoch "$epoch" '$epoch | todateiso8601')"
readonly commit epoch created
module_version=''
for target in "${targets[@]}"; do
  settings="$(native_target "$target")"
  IFS=$'\t' read -r goos goarch tuning_key tuning <<<"$settings"
  extension=''
  if [[ "$goos" == windows ]]; then
    extension=.exe
  fi
  candidate="$directory/bargeboard-native-candidate-$target$extension"
  reference="$directory/bargeboard-native-reference-$target$extension"
  if ! cmp -s "$candidate" "$reference"; then
    printf 'native candidate differs from independent reference: %s\n' "$target" >&2
    exit 1
  fi
  info="$(go version -m -json "$reference")"
  if [[ -z "$module_version" ]]; then
    module_version="$(jq -er '
      .Main.Version | select(type == "string" and length > 0 and . != "(devel)")
    ' <<<"$info")"
    if [[ -n "${EXPECTED_TAG:-}" && "$module_version" != "$EXPECTED_TAG" ]]; then
      printf 'native module version does not match release tag: %s\n' "$module_version" >&2
      exit 1
    fi
  fi
  if ! printf '%s\n' "$info" | bash "$script_dir/validate-release-binary.sh" \
    "${EXPECTED_GO_VERSION:-go1.27.4}" "$goos" "$goarch" "$tuning_key" "$tuning" \
    "$commit" "${EXPECTED_VCS_MODIFIED:-false}" "$created" "$module_version"; then
    printf 'native input metadata does not match release policy: %s\n' "$target" >&2
    exit 1
  fi
done
printf '%s\n' "$module_version"
