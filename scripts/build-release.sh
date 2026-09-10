#!/usr/bin/env bash
set -euo pipefail

readonly tag="${1:?usage: build-release.sh TAG}"

export GORELEASER_CURRENT_TAG="$tag"
exec goreleaser release --clean
