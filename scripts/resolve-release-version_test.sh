#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
readonly resolver="$root/scripts/resolve-release-version.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
readonly work
mkdir "$work/source"
cp "$root/.goreleaser-version.yaml" "$work/source/.goreleaser-version.yaml"
printf 'module example.invalid/version\n\ngo 1.27.0\n' >"$work/source/go.mod"
printf 'package main\n\nfunc main() {}\n' >"$work/source/main.go"
printf 'build/\n' >"$work/source/.gitignore"
git -C "$work/source" init -q
git -C "$work/source" remote add origin https://github.com/example/synthetic-version.git
git -C "$work/source" add .
git -C "$work/source" -c user.name='Version Test' -c user.email='version@example.invalid' \
  commit --no-gpg-sign -qm 'Create the synthetic snapshot fixture'
git -C "$work/source" -c tag.gpgSign=false tag last-source
(
  cd "$work/source"
  actual="$(bash "$resolver")"
  expected="last-source-SNAPSHOT-$(git rev-parse --short=7 HEAD)"
  if [[ "$actual" != "$expected" ]]; then
    printf 'resolved snapshot = <%s>, want <%s>\n' "$actual" "$expected" >&2
    exit 1
  fi
  jq -e 'map({name,path,type}) == [{
    name:"metadata.json",path:"build/native-version/metadata.json",type:"Metadata"
  }]' build/native-version/artifacts.json >/dev/null
)

# Exercise malformed tool output without substituting a version formula in production.
mkdir "$work/bin"
cat >"$work/bin/goreleaser" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
mkdir -p build/native-version
printf '%s' "$TEST_METADATA" >build/native-version/metadata.json
exit "${TEST_STATUS:-0}"
EOF
chmod +x "$work/bin/goreleaser"
commit="$(git -C "$work/source" rev-parse HEAD)"
valid="$(jq -cn --arg commit "$commit" '{commit:$commit,project_name:"bargeboard",version:"1.2.3-SNAPSHOT-fixture"}')"
for mutation in \
  '.commit = "different"' \
  '.project_name = "different"' \
  '.version = ""' \
  '.version = 123' \
  '.version = "../unsafe"' \
  '.version = "1.0.0\nsecond"' \
  '.version = ("a" * 131)'; do
  metadata="$(jq -c "$mutation" <<<"$valid")"
  if (
    cd "$work/source"
    PATH="$work/bin:$PATH" TEST_METADATA="$metadata" bash "$resolver"
  ) >"$work/error" 2>&1; then
    printf 'accepted invalid snapshot metadata: %s\n' "$mutation" >&2
    exit 1
  fi
done
if (
  cd "$work/source"
  PATH="$work/bin:$PATH" TEST_METADATA="$valid" TEST_STATUS=7 bash "$resolver"
) >"$work/error" 2>&1; then
  printf 'accepted a failed GoReleaser invocation\n' >&2
  exit 1
fi
