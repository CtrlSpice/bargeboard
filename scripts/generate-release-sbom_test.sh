#!/usr/bin/env bash
set -euo pipefail

work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"

cat >"$work/bin/syft" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
touch "$SYFT_CALLED"
EOF
chmod +x "$work/bin/syft"

archive="$work/bargeboard_v1.2.3_linux_amd64.tar.gz"
readonly archive
dd if=/dev/null of="$archive" bs=1 seek=$((128 * 1024 * 1024)) count=1 2>/dev/null

output=""
if output="$(
  env SYFT_CALLED="$work/syft-called" PATH="$work/bin:$PATH" \
    bash scripts/generate-release-sbom.sh "$archive" "$work/sbom.json" 2>&1
)"; then
  printf 'SBOM generation accepted an oversized archive\n' >&2
  exit 1
fi
if [[ -e "$work/syft-called" ]]; then
  printf 'Syft scanned an archive before canonical validation\n' >&2
  exit 1
fi
if [[ "$output" != *'refusing to scan a noncanonical release archive'* ]]; then
  printf 'SBOM preflight did not report canonical validation failure:\n%s\n' "$output" >&2
  exit 1
fi
