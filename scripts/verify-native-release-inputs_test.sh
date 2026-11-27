#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
readonly verifier="$root/scripts/verify-native-release-inputs.sh"
work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin" "$work/valid"
# The build and build-info validator suites exercise real CGO binaries. This
# fixture supplies independent build metadata for all four transfer targets.
cat >"$work/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ $# == 4 && "$1" == version && "$2" == -m && "$3" == -json ]]
cat "$4"
exit "${TEST_GO_STATUS:-0}"
EOF
chmod +x "$work/bin/go"
export PATH="$work/bin:$PATH"
export EXPECTED_COMMIT
EXPECTED_COMMIT="$(git rev-parse HEAD)"
epoch="$(git show -s --format=%ct HEAD)"
created="$(jq -nr --argjson epoch "$epoch" '$epoch | todateiso8601')"
for record in \
  'darwin_arm64_v8.0 darwin arm64 GOARM64 v8.0' \
  'linux_amd64_v1 linux amd64 GOAMD64 v1' \
  'linux_arm64_v8.0 linux arm64 GOARM64 v8.0' \
  'windows_amd64_v1 windows amd64 GOAMD64 v1'; do
  read -r target goos goarch tuning_key tuning <<<"$record"
  extension=''
  if [[ "$goos" == windows ]]; then
    extension=.exe
  fi
  for kind in candidate reference; do
    jq -n --arg commit "$EXPECTED_COMMIT" --arg created "$created" \
      --arg goos "$goos" --arg goarch "$goarch" --arg key "$tuning_key" --arg tuning "$tuning" '{
      GoVersion:"go1.27.4",
      Path:"github.com/CtrlSpice/bargeboard",
      Main:{Path:"github.com/CtrlSpice/bargeboard",Version:"v1.2.3"},
      Deps:[],
      Settings:[
        {Key:"-buildmode",Value:"exe"}, {Key:"-trimpath",Value:"true"},
        {Key:"CGO_ENABLED",Value:"1"}, {Key:"GOOS",Value:$goos},
        {Key:"GOARCH",Value:$goarch}, {Key:$key,Value:$tuning},
        {Key:"vcs",Value:"git"}, {Key:"vcs.revision",Value:$commit},
        {Key:"vcs.time",Value:$created}, {Key:"vcs.modified",Value:"false"}
      ]
    }' >"$work/valid/bargeboard-native-$kind-$target$extension"
  done
done
cp -R "$work/valid" "$work/original"
test "$(bash "$verifier" "$work/valid")" = v1.2.3
test "$(EXPECTED_TAG=v1.2.3 bash "$verifier" "$work/valid")" = v1.2.3
diff -r "$work/original" "$work/valid"

reject() {
  local name="$1" message="$2" status=0
  bash "$verifier" "$work/$name" >"$work/stdout" 2>"$work/stderr" || status=$?
  if [[ "$status" == 0 || -s "$work/stdout" ]]; then
    printf 'native input unexpectedly accepted %s, status=%s\n' "$name" "$status" >&2
    exit 1
  fi
  if [[ -n "$message" ]] && ! grep -F "$message" "$work/stderr" >/dev/null; then
    cat "$work/stderr" >&2
    printf 'native input rejection missed %s: %s\n' "$name" "$message" >&2
    exit 1
  fi
}
readonly last=bargeboard-native-reference-windows_amd64_v1.exe
for name in missing extra hidden nested empty symlink directory different; do
  cp -R "$work/valid" "$work/$name"
done
rm "$work/missing/$last"
touch "$work/extra/unexpected" "$work/hidden/.unexpected"
mkdir "$work/nested/subdirectory"
reject missing 'exactly the four candidate and four reference binaries'
reject extra 'exactly the four candidate and four reference binaries'
reject hidden 'exactly the four candidate and four reference binaries'
reject nested 'exactly the four candidate and four reference binaries'
for name in empty symlink directory; do
  rm "$work/$name/$last"
done
touch "$work/empty/$last"
ln -s "$work/valid/$last" "$work/symlink/$last"
mkdir "$work/directory/$last"
reject empty 'not a nonempty regular file'
reject symlink 'not a nonempty regular file'
reject directory 'not a nonempty regular file'
printf '\n' >>"$work/different/$last"
reject different 'native candidate differs from independent reference: windows_amd64_v1'
ln -s "$work/valid" "$work/linked-root"
reject linked-root 'native input must be a regular directory'
reject absent 'native input must be a regular directory'

for mutation in \
  '.Main.Version = "v9.9.9"' \
  '(.Settings[] | select(.Key == "CGO_ENABLED") | .Value) = "0"' \
  '(.Settings[] | select(.Key == "GOARCH") | .Value) = "arm64"' \
  '(.Settings[] | select(.Key == "vcs.revision") | .Value) = ("a" * 40)' \
  '(.Settings[] | select(.Key == "vcs.modified") | .Value) = "true"'; do
  mkdir "$work/mutated"
  cp "$work/valid/"* "$work/mutated/"
  for kind in candidate reference; do
    name="bargeboard-native-$kind-windows_amd64_v1.exe"
    jq "$mutation" "$work/valid/$name" >"$work/mutated/$name"
  done
  reject mutated 'native input metadata does not match release policy: windows_amd64_v1'
  rm -rf "$work/mutated"
done
EXPECTED_TAG=v9.9.9 reject valid 'native module version does not match release tag'
TEST_GO_STATUS=7 reject valid ''
diff -r "$work/original" "$work/valid"
