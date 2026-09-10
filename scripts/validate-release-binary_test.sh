#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
readonly validator="$script_dir/validate-release-binary.sh"
readonly commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT

fixture() {
  jq -cn --arg commit "$commit" '{
    GoVersion: "go1.26.8",
    Path: "github.com/CtrlSpice/bargeboard",
    Main: {
      Path: "github.com/CtrlSpice/bargeboard",
      Version: "v1.2.3"
    },
    Deps: [{Path: "example.com/dependency", Version: "v1.0.0"}],
    Settings: [
      {Key: "-buildmode", Value: "exe"},
      {Key: "-trimpath", Value: "true"},
      {Key: "CGO_ENABLED", Value: "0"},
      {Key: "GOOS", Value: "linux"},
      {Key: "GOARCH", Value: "amd64"},
      {Key: "GOAMD64", Value: "v1"},
      {Key: "vcs", Value: "git"},
      {Key: "vcs.revision", Value: $commit},
      {Key: "vcs.time", Value: "2026-09-09T00:00:00Z"},
      {Key: "vcs.modified", Value: "false"}
    ]
  }'
}

validate() {
  bash "$validator" go1.26.8 linux amd64 GOAMD64 v1 "$commit" false 2026-09-09T00:00:00Z v1.2.3
}

reject() {
  local description="${1:?description required}"
  local filter="${2:?jq filter required}"
  if fixture | jq -c "$filter" |
    bash "$validator" \
      go1.26.8 linux amd64 GOAMD64 v1 "$commit" false \
      2026-09-09T00:00:00Z v1.2.3 >/dev/null 2>&1; then
    printf 'expected invalid release binary metadata: %s\n' "$description" >&2
    exit 1
  fi
}

fixture | validate
fixture | bash "$validator" go1.26.8 linux amd64 GOAMD64 v1 "$commit" "" 2026-09-09T00:00:00Z v1.2.3
fixture | jq -c --arg commit "$commit" \
  '.Main.Version = ("v1.2.4-0.20260909000000-" + $commit[0:12])' |
  bash "$validator" go1.26.8 linux amd64 GOAMD64 v1 "$commit" false 2026-09-09T00:00:00Z v1.2.4-0.20260909000000-aaaaaaaaaaaa
fixture | jq -c --arg commit "$commit" \
  '.Main.Version = ("v0.0.0-20260909000000-" + $commit[0:12])' |
  bash "$validator" go1.26.8 linux amd64 GOAMD64 v1 "$commit" false 2026-09-09T00:00:00Z v0.0.0-20260909000000-aaaaaaaaaaaa

mkdir "$work/module"
printf 'module github.com/CtrlSpice/bargeboard\n\ngo 1.26\n' >"$work/module/go.mod"
printf 'package main\n\nfunc main() {}\n' >"$work/module/main.go"
git -C "$work/module" init -q
git -C "$work/module" add go.mod main.go
GIT_AUTHOR_DATE=2026-09-09T00:00:00Z \
  GIT_COMMITTER_DATE=2026-09-09T00:00:00Z \
  git -C "$work/module" \
    -c commit.gpgsign=false \
    -c user.name='Release Test' \
    -c user.email='release-test@example.invalid' \
    commit -q -m fixture
git -C "$work/module" \
  -c tag.gpgSign=false \
  -c user.name='Release Test' \
  -c user.email='release-test@example.invalid' \
  tag -a -m fixture v1.2.3+build.1
(
  cd "$work/module"
  CGO_ENABLED=0 go build -buildvcs=true -trimpath -o "$work/bargeboard" .
)
real_commit="$(git -C "$work/module" rev-parse HEAD)"
readonly real_commit
real_build_info="$(go version -m -json "$work/bargeboard")"
readonly real_build_info
real_module_version="$(jq -er '.Main.Version | select(. != "(devel)")' <<<"$real_build_info")"
readonly real_module_version
real_go_version="$(go env GOVERSION)"
real_goos="$(go env GOOS)"
real_goarch="$(go env GOARCH)"
case "$real_goarch" in
  amd64)
    real_tuning_key=GOAMD64
    real_tuning="$(go env GOAMD64)"
    ;;
  arm64)
    real_tuning_key=GOARM64
    real_tuning="$(go env GOARM64)"
    ;;
  *)
    printf 'unsupported release binary test architecture: %s\n' "$real_goarch" >&2
    exit 1
    ;;
esac
printf '%s\n' "$real_build_info" | jq -c '.Deps = []' |
  bash "$validator" \
    "$real_go_version" \
    "$real_goos" \
    "$real_goarch" \
    "$real_tuning_key" \
    "$real_tuning" \
    "$real_commit" \
    false \
    2026-09-09T00:00:00Z \
    "$real_module_version"

git -C "$work/module" tag -d v1.2.3+build.1 >/dev/null
git -C "$work/module" \
  -c tag.gpgSign=false \
  -c user.name='Release Test' \
  -c user.email='release-test@example.invalid' \
  tag -a -m fixture v2.0.0
(
  cd "$work/module"
  CGO_ENABLED=0 go build -a -buildvcs=true -trimpath -o "$work/bargeboard-v2" .
)
v2_build_info="$(go version -m -json "$work/bargeboard-v2")"
readonly v2_build_info
v2_module_version="$(jq -er '.Main.Version | select(. != "(devel)")' <<<"$v2_build_info")"
readonly v2_module_version
printf '%s\n' "$v2_build_info" | jq -c '.Deps = []' |
  bash "$validator" \
    "$real_go_version" \
    "$real_goos" \
    "$real_goarch" \
    "$real_tuning_key" \
    "$real_tuning" \
    "$real_commit" \
    false \
    2026-09-09T00:00:00Z \
    "$v2_module_version"

reject 'Go version' '.GoVersion = "go1.27.0"'
reject 'command path' '.Path = "example.com/other"'
reject 'module path' '.Main.Path = "example.com/other"'
reject 'module version' '.Main.Version = "v9.9.9"'
reject 'missing dependency inventory' 'del(.Deps)'
reject 'build mode' '(.Settings[] | select(.Key == "-buildmode") | .Value) = "pie"'
reject 'trimpath' '(.Settings[] | select(.Key == "-trimpath") | .Value) = "false"'
reject 'CGO' '(.Settings[] | select(.Key == "CGO_ENABLED") | .Value) = "1"'
reject 'operating system' '(.Settings[] | select(.Key == "GOOS") | .Value) = "darwin"'
reject 'architecture' '(.Settings[] | select(.Key == "GOARCH") | .Value) = "arm64"'
reject 'tuning' '(.Settings[] | select(.Key == "GOAMD64") | .Value) = "v3"'
reject 'VCS implementation' '(.Settings[] | select(.Key == "vcs") | .Value) = "other"'
reject 'commit' '(.Settings[] | select(.Key == "vcs.revision") | .Value) = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"'
reject 'commit time' '(.Settings[] | select(.Key == "vcs.time") | .Value) = "2099-01-01T00:00:00Z"'
reject 'dirty worktree' '(.Settings[] | select(.Key == "vcs.modified") | .Value) = "true"'
reject 'missing setting' '.Settings = [.Settings[] | select(.Key != "GOOS")]'
