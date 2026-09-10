#!/usr/bin/env bash
set -euo pipefail

work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT

mkdir "$work/bin" "$work/capture"
cat >"$work/bin/goreleaser" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${GORELEASER_CURRENT_TAG:-}" >"$CAPTURE/tag"
printf '%s\n' "$@" >"$CAPTURE/arguments"
EOF
chmod +x "$work/bin/goreleaser"

CAPTURE="$work/capture" PATH="$work/bin:$PATH" bash scripts/build-release.sh v1.2.3

if [[ "$(<"$work/capture/tag")" != v1.2.3 ]]; then
  printf 'GoReleaser was not bound to the requested release tag\n' >&2
  exit 1
fi
if [[ "$(<"$work/capture/arguments")" != $'release\n--clean' ]]; then
  printf 'unexpected GoReleaser arguments\n' >&2
  exit 1
fi
if bash scripts/build-release.sh >/dev/null 2>&1; then
  printf 'build-release accepted a missing tag\n' >&2
  exit 1
fi
