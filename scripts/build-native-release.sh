#!/usr/bin/env bash
set -euo pipefail

native_target() {
  case "${1:-}" in
    linux_amd64_v1) printf 'linux\tamd64\tGOAMD64\tv1\n' ;;
    linux_arm64_v8.0) printf 'linux\tarm64\tGOARM64\tv8.0\n' ;;
    darwin_arm64_v8.0) printf 'darwin\tarm64\tGOARM64\tv8.0\n' ;;
    windows_amd64_v1) printf 'windows\tamd64\tGOAMD64\tv1\n' ;;
    *) printf 'unsupported native release target: %s\n' "${1:-}" >&2; return 1 ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

if [[ $# != 3 ]]; then
  printf 'usage: build-native-release.sh TARGET VERSION OUTPUT\n' >&2
  exit 1
fi
readonly target="$1" version="$2" output="$3"
if [[ ! "$version" =~ ^[0-9A-Za-z][0-9A-Za-z.+-]*$ || ${#version} -gt 130 ]]; then
  printf 'invalid native release version: %s\n' "$version" >&2
  exit 1
fi
settings="$(native_target "$target")"
IFS=$'\t' read -r goos goarch tuning_key tuning <<<"$settings"
readonly settings goos goarch tuning_key tuning
export GOTOOLCHAIN=local
if [[ "$(go env GOVERSION)" != go1.27.4 ||
  "$(go env GOHOSTOS)/$(go env GOHOSTARCH)" != "$goos/$goarch" ]]; then
  printf 'native release requires Go 1.27.4 on %s/%s\n' "$goos" "$goarch" >&2
  exit 1
fi

export CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch"
export "$tuning_key=$tuning"
export CGO_CFLAGS='-O2 -g' CGO_CXXFLAGS='-O2 -g' CGO_LDFLAGS='-O2 -g'
case "$goos" in
  darwin)
    # Explicit flags also keep Go from reusing C objects built for another target.
    export MACOSX_DEPLOYMENT_TARGET=13.0
    export CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0'
    export CGO_CXXFLAGS='-O2 -g -mmacosx-version-min=13.0'
    export CGO_LDFLAGS='-O2 -g -mmacosx-version-min=13.0'
    ;;
  windows)
    export CC='C:/mingw64/bin/gcc.exe' CXX='C:/mingw64/bin/g++.exe'
    ;;
esac

if [[ -L "$output" || -d "$output" ]]; then
  printf 'native output must be a regular file: %s\n' "$output" >&2
  exit 1
fi
mkdir -p "$(dirname "$output")"
go build -buildvcs=true -mod=readonly -trimpath \
  -ldflags "-s -w -X main.version=$version" -o "$output" .

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
commit="$(git rev-parse --verify 'HEAD^{commit}')"
epoch="$(git show -s --format=%ct HEAD)"
created="$(jq -nr --argjson epoch "$epoch" '$epoch | todateiso8601')"
build_info="$(go version -m -json "$output")"
module_version="$(jq -er '.Main.Version | select(type == "string" and length > 0 and . != "(devel)")' <<<"$build_info")"
readonly script_dir commit epoch created build_info module_version
if [[ -n "${EXPECTED_TAG:-}" && "$module_version" != "$EXPECTED_TAG" ]]; then
  printf 'native module version does not match release tag: %s\n' "$module_version" >&2
  exit 1
fi
printf '%s\n' "$build_info" | bash "$script_dir/validate-release-binary.sh" \
  go1.27.4 "$goos" "$goarch" "$tuning_key" "$tuning" "$commit" \
  "${EXPECTED_VCS_MODIFIED:-false}" "$created" "$module_version"

actual_version="$("$output" --version)"
if [[ "$actual_version" != "bargeboard version $version" ]]; then
  printf 'unexpected native binary version: %s\n' "$actual_version" >&2
  exit 1
fi
"$output" validate --config config.yaml
printf '%s\n' "$build_info" | jq '{GoVersion, Main, Settings}'
case "$goos" in
  darwin)
    minimum="$(otool -l "$output" | awk '$1 == "minos" { print $2 }')"
    if [[ "$minimum" != 13.0 ]]; then
      printf 'unexpected macOS deployment target: %s\n' "$minimum" >&2
      exit 1
    fi
    otool -L "$output"
    ;;
  linux)
    readelf -d "$output"
    readelf --version-info "$output" | awk '/Version needs|Name:/'
    ;;
  windows)
    'C:/mingw64/bin/objdump.exe' -p "$output" | awk '/DLL Name:/'
    ;;
esac
