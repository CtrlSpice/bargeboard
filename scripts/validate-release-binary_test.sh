#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
readonly validator="$script_dir/validate-release-binary.sh"
readonly commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

fixture() {
  jq -cn --arg commit "$commit" '{
    GoVersion: "go1.26.8",
    Path: "github.com/CtrlSpice/bargeboard",
    Main: {
      Path: "github.com/CtrlSpice/bargeboard",
      Version: "v1.2.3"
    },
    Settings: [
      {Key: "-buildmode", Value: "exe"},
      {Key: "-trimpath", Value: "true"},
      {Key: "CGO_ENABLED", Value: "0"},
      {Key: "GOOS", Value: "linux"},
      {Key: "GOARCH", Value: "amd64"},
      {Key: "GOAMD64", Value: "v1"},
      {Key: "vcs.revision", Value: $commit},
      {Key: "vcs.modified", Value: "false"}
    ]
  }'
}

validate() {
  bash "$validator" go1.26.8 linux amd64 GOAMD64 v1 "$commit" false
}

reject() {
  local description="${1:?description required}"
  local filter="${2:?jq filter required}"
  if fixture | jq -c "$filter" | validate >/dev/null 2>&1; then
    printf 'expected invalid release binary metadata: %s\n' "$description" >&2
    exit 1
  fi
}

fixture | validate
fixture | bash "$validator" go1.26.8 linux amd64 GOAMD64 v1 "$commit" ""

reject 'Go version' '.GoVersion = "go1.27.0"'
reject 'command path' '.Path = "example.com/other"'
reject 'module path' '.Main.Path = "example.com/other"'
reject 'build mode' '(.Settings[] | select(.Key == "-buildmode") | .Value) = "pie"'
reject 'trimpath' '(.Settings[] | select(.Key == "-trimpath") | .Value) = "false"'
reject 'CGO' '(.Settings[] | select(.Key == "CGO_ENABLED") | .Value) = "1"'
reject 'operating system' '(.Settings[] | select(.Key == "GOOS") | .Value) = "darwin"'
reject 'architecture' '(.Settings[] | select(.Key == "GOARCH") | .Value) = "arm64"'
reject 'tuning' '(.Settings[] | select(.Key == "GOAMD64") | .Value) = "v3"'
reject 'commit' '(.Settings[] | select(.Key == "vcs.revision") | .Value) = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"'
reject 'dirty worktree' '(.Settings[] | select(.Key == "vcs.modified") | .Value) = "true"'
reject 'missing setting' '.Settings = [.Settings[] | select(.Key != "GOOS")]'
