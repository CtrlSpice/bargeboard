#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly builder="$script_dir/build-native-release.sh"
source "$builder"

assert_target() {
  local target="$1" expected="$2" actual
  actual="$(native_target "$target")"
  if [[ "$actual" != "$expected" ]]; then
    printf 'native target %s resolved to <%s>, want <%s>\n' "$target" "$actual" "$expected" >&2
    exit 1
  fi
}
assert_target linux_amd64_v1 $'linux\tamd64\tGOAMD64\tv1'
assert_target linux_arm64_v8.0 $'linux\tarm64\tGOARM64\tv8.0'
assert_target darwin_arm64_v8.0 $'darwin\tarm64\tGOARM64\tv8.0'
assert_target windows_amd64_v1 $'windows\tamd64\tGOAMD64\tv1'
for target in '' darwin_amd64_v1 windows_arm64_v8.0 linux_amd64_v3 linux_arm64 linux_arm64_v8.0/extra; do
  if native_target "$target" >/dev/null 2>&1; then
    printf 'accepted unsupported native target: %s\n' "$target" >&2
    exit 1
  fi
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
readonly work
for version in '' '../v1' '1.0.0 -X main.version=wrong' $'1.0.0\nother' "$(printf '%0131d' 0)"; do
  if bash "$builder" linux_amd64_v1 "$version" "$work/rejected/output" >"$work/error" 2>&1; then
    printf 'accepted invalid native version: %q\n' "$version" >&2
    exit 1
  fi
  grep -F 'invalid native release version:' "$work/error" >/dev/null
  test ! -e "$work/rejected"
done
case "$(go env GOHOSTOS)/$(go env GOHOSTARCH)" in
  darwin/arm64) target=darwin_arm64_v8.0; foreign=linux_amd64_v1 ;;
  linux/amd64) target=linux_amd64_v1; foreign=darwin_arm64_v8.0 ;;
  linux/arm64) target=linux_arm64_v8.0; foreign=darwin_arm64_v8.0 ;;
  windows/amd64) target=windows_amd64_v1; foreign=darwin_arm64_v8.0 ;;
  *) printf 'unsupported native test host\n' >&2; exit 1 ;;
esac
if bash "$builder" "$foreign" 1.2.3 "$work/rejected/output" >"$work/error" 2>&1; then
  printf 'accepted a cross-platform native release build\n' >&2
  exit 1
fi
grep -F 'native release requires Go 1.27.4 on' "$work/error" >/dev/null
test ! -e "$work/rejected"

mkdir -p "$work/source/dep"
cat >"$work/source/go.mod" <<'EOF'
module github.com/CtrlSpice/bargeboard

go 1.27.0

require example.invalid/nativeprobe v0.0.0
replace example.invalid/nativeprobe => ./dep
EOF
printf 'module example.invalid/nativeprobe\n\ngo 1.27.0\n' >"$work/source/dep/go.mod"
printf 'package nativeprobe\n' >"$work/source/dep/probe.go"
printf 'synthetic native configuration\n' >"$work/source/config.yaml"
cat >"$work/source/main.go" <<'EOF'
package main

/*
static int native_value(void) { return 42; }
*/
import "C"

import (
	"fmt"
	"os"

	_ "example.invalid/nativeprobe"
)

var version string

func main() {
	if C.native_value() != 42 {
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("bargeboard version " + version)
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "validate" && os.Args[2] == "--config" {
		data, err := os.ReadFile(os.Args[3])
		if err == nil && string(data) == "synthetic native configuration\n" {
			return
		}
		fmt.Fprintln(os.Stderr, "invalid synthetic native configuration")
	}
	os.Exit(1)
}
EOF
git -C "$work/source" init -q
git -C "$work/source" add .
git -C "$work/source" -c user.name='Native Test' -c user.email='native@example.invalid' \
  commit --no-gpg-sign -qm 'Create the synthetic native build fixture'
git clone --quiet --no-hardlinks "$work/source" "$work/reference"
extension=''
if [[ "$target" == windows_* ]]; then
  extension=.exe
fi
(
  cd "$work/source"
  GOCACHE="$work/candidate-go-build" GOMODCACHE="$work/candidate-go-mod" \
    bash "$builder" "$target" 1.2.3 "$work/candidate$extension" >"$work/candidate.log"
)
(
  cd "$work/reference"
  GOCACHE="$work/reference-go-build" GOMODCACHE="$work/reference-go-mod" \
    bash "$builder" "$target" 1.2.3 "$work/reference-binary$extension" >"$work/reference.log"
)
cmp "$work/candidate$extension" "$work/reference-binary$extension"

printf 'invalid\n' >"$work/source/config.yaml"
if (
  cd "$work/source"
  EXPECTED_VCS_MODIFIED=true bash "$builder" "$target" 1.2.3 "$work/invalid$extension"
) >"$work/error" 2>&1; then
  printf 'native build accepted an invalid shipped configuration\n' >&2
  exit 1
fi
grep -F 'invalid synthetic native configuration' "$work/error" >/dev/null
