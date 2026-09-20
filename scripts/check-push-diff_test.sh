#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
readonly helper="$script_dir/check-push-diff.sh"
work="$(mktemp -d)"
readonly work
trap 'rm -rf "$work"' EXIT

# Keep all Git state, identities, hooks, configuration and transport local to
# disposable fixtures. No user configuration or credentials are needed.
export LC_ALL=C
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE
unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_CONFIG_PARAMETERS
unset GIT_NO_LAZY_FETCH
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_COUNT=0
export GIT_AUTHOR_NAME='Synthetic CI Test' GIT_AUTHOR_EMAIL='ci@example.invalid'
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME" GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"
export GIT_AUTHOR_DATE='2000-01-01T00:00:00Z'
export GIT_COMMITTER_DATE="$GIT_AUTHOR_DATE"
export GIT_ALLOW_PROTOCOL= GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0
export HOME="$work/home" XDG_CONFIG_HOME="$work/home"
mkdir "$HOME" "$work/template"
git init -q --object-format=sha1 --template="$work/template" "$work/repo"
cd "$work/repo"

printf 'base\n' >base.txt
git add base.txt
git commit --no-gpg-sign -qm 'Create clean base'
base="$(git rev-parse HEAD)"
printf 'clean addition\n' >added.txt
git add added.txt
git commit --no-gpg-sign -qm 'Add clean file'
clean="$(git rev-parse HEAD)"
printf 'inherited trailing space \n' >inherited.txt
git add inherited.txt
git commit --no-gpg-sign -qm 'Add whitespace fixture'
dirty="$(git rev-parse HEAD)"
printf 'another clean line\n' >>added.txt
git add added.txt
git commit --no-gpg-sign -qm 'Keep inherited whitespace unchanged'
inherited="$(git rev-parse HEAD)"

blob="$(git rev-parse HEAD:base.txt)"
tree="$(git rev-parse 'HEAD^{tree}')"
git tag --no-sign -a fixture -m 'Synthetic annotated tag' "$clean"
tag="$(git rev-parse refs/tags/fixture)"
missing=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
if git cat-file -e "$missing" 2>/dev/null; then
  printf 'missing-object fixture unexpectedly exists\n' >&2
  exit 1
fi

# An unreadable object is an error, not permission to use the full-tree check.
broken="$(printf 'corrupt-object fixture\n' | git hash-object -w --stdin)"
chmod u+w ".git/objects/${broken:0:2}/${broken:2}"
printf 'not a Git object\n' >".git/objects/${broken:0:2}/${broken:2}"

# A real unrelated staged change, unstaged change and untracked file must all
# survive every accepted and rejected invocation, along with refs and objects.
printf 'staged\n' >base.txt
git add base.txt
printf 'unstaged\n' >>base.txt
printf 'untracked\n' >untracked.txt
state_dir="$work/repo"
snapshot_dir="$work/unchanged"
cp -R "$state_dir" "$snapshot_dir"

tests=0
expect() {
  local expected="$1" description="$2" diagnostic="$3"
  shift 3
  local status=0
  : >"$work/trace"
  env -u BEFORE_SHA -u HEAD_SHA -u PUSH_FORCED \
    GIT_TRACE="$work/trace" "$@" bash "$helper" >"$work/output" 2>&1 || status=$?
  if grep -Eq '(^|[[:space:]])(fetch|fetch-pack|remote-[^[:space:]]+)([[:space:]]|$)' "$work/trace"; then
    printf '%s: attempted a fetch or remote transport\n' "$description" >&2
    exit 1
  fi
  if [[ "$status" != "$expected" ]]; then
    printf '%s: expected exit %s, got %s\n' "$description" "$expected" "$status" >&2
    cat "$work/output" >&2
    exit 1
  fi
  if [[ -n "$diagnostic" ]] && ! grep -Fq -- "$diagnostic" "$work/output"; then
    printf '%s: missing diagnostic: %s\n' "$description" "$diagnostic" >&2
    cat "$work/output" >&2
    exit 1
  fi
  if [[ "$expected" == 0 && -s "$work/output" ]]; then
    printf '%s: unexpected output on success\n' "$description" >&2
    cat "$work/output" >&2
    exit 1
  fi
  if ! diff -r "$snapshot_dir" "$state_dir"; then
    printf '%s: changed repository state\n' "$description" >&2
    exit 1
  fi
  tests=$((tests + 1))
}

for forced in false true; do
  expect 0 "existing before, clean, forced=$forced" '' \
    BEFORE_SHA="$base" HEAD_SHA="$clean" PUSH_FORCED="$forced"
  expect 2 "existing before, new whitespace, forced=$forced" 'inherited.txt:1: trailing whitespace.' \
    BEFORE_SHA="$clean" HEAD_SHA="$dirty" PUSH_FORCED="$forced"
  expect 0 "existing before, inherited whitespace, forced=$forced" '' \
    BEFORE_SHA="$dirty" HEAD_SHA="$inherited" PUSH_FORCED="$forced"
done

expect 0 'missing before, forced, clean tree' '' \
  BEFORE_SHA="$missing" HEAD_SHA="$clean" PUSH_FORCED=true
expect 2 'missing before, forced, new whitespace' 'inherited.txt:1: trailing whitespace.' \
  BEFORE_SHA="$missing" HEAD_SHA="$dirty" PUSH_FORCED=true
expect 2 'missing before, forced, inherited whitespace' 'inherited.txt:1: trailing whitespace.' \
  BEFORE_SHA="$missing" HEAD_SHA="$inherited" PUSH_FORCED=true
expect 1 'missing before, ordinary push' 'BEFORE_SHA is unavailable for a nonforced push' \
  BEFORE_SHA="$missing" HEAD_SHA="$clean" PUSH_FORCED=false

for before in "$base" "$missing"; do
  expect 1 'unset forced flag' 'PUSH_FORCED required' BEFORE_SHA="$before" HEAD_SHA="$clean"
  for forced in '' TRUE False 1 yes 'true '; do
    expect 1 'empty or invalid forced flag' 'PUSH_FORCED' \
      BEFORE_SHA="$before" HEAD_SHA="$clean" PUSH_FORCED="$forced"
  done
done

expect 1 'unset before' 'BEFORE_SHA required' HEAD_SHA="$clean" PUSH_FORCED=true
expect 1 'unset head' 'HEAD_SHA required' BEFORE_SHA="$missing" PUSH_FORCED=true
for invalid in '' --help HEAD "${base:0:12}" "${base}^" "${base}0" \
  gggggggggggggggggggggggggggggggggggggggg "${base}"$'\n'; do
  expect 1 'empty or malformed before' 'BEFORE_SHA' \
    BEFORE_SHA="$invalid" HEAD_SHA="$clean" PUSH_FORCED=true
  expect 1 'empty or malformed head' 'HEAD_SHA' \
    BEFORE_SHA="$missing" HEAD_SHA="$invalid" PUSH_FORCED=true
done

for before in "$base" "$missing"; do
  expect 128 'unavailable head' '' BEFORE_SHA="$before" HEAD_SHA="$missing" PUSH_FORCED=true
  for noncommit in "$blob" "$tree" "$tag"; do
    expect 1 'noncommit head' 'HEAD_SHA must identify a commit object' \
      BEFORE_SHA="$before" HEAD_SHA="$noncommit" PUSH_FORCED=true
  done
done
for noncommit in "$blob" "$tree" "$tag"; do
  expect 1 'present noncommit before' 'BEFORE_SHA must identify a commit object' \
    BEFORE_SHA="$noncommit" HEAD_SHA="$clean" PUSH_FORCED=true
done
expect 1 'corrupt before object' 'BEFORE_SHA must identify a commit object' \
  BEFORE_SHA="$broken" HEAD_SHA="$clean" PUSH_FORCED=true
expect 128 'corrupt head object' '' BEFORE_SHA="$missing" HEAD_SHA="$broken" PUSH_FORCED=true

# A real partial clone can lazily fetch a missing commit from its promisor.
# Only local file transport is allowed, so this remains entirely offline.
mkdir "$work/promisor"
git init -q --object-format=sha1 --template="$work/template" "$work/promisor/remote"
git -C "$work/promisor/remote" config uploadpack.allowFilter true
printf 'local promisor base\n' >"$work/promisor/remote/base.txt"
git -C "$work/promisor/remote" add base.txt
git -C "$work/promisor/remote" commit --no-gpg-sign -qm 'Create local promisor base'
GIT_ALLOW_PROTOCOL=file git clone -q --no-local --filter=blob:none \
  --template="$work/template" "$work/promisor/remote" "$work/promisor/repo"
test "$(git -C "$work/promisor/repo" config --get remote.origin.promisor)" = true
promisor_head="$(git -C "$work/promisor/repo" rev-parse HEAD)"

# Create the before commit after cloning: it exists only in the local remote.
printf 'remote-only addition\n' >"$work/promisor/remote/remote-only.txt"
git -C "$work/promisor/remote" add remote-only.txt
git -C "$work/promisor/remote" commit --no-gpg-sign -qm 'Create remote-only before commit'
promisor_before="$(git -C "$work/promisor/remote" rev-parse HEAD)"
if GIT_NO_LAZY_FETCH=1 git -C "$work/promisor/repo" cat-file -e "$promisor_before" 2>/dev/null; then
  printf 'remote-only before commit unexpectedly exists in partial clone\n' >&2
  exit 1
fi

# Compare both repositories, including their objects, index, refs and worktree.
state_dir="$work/promisor"
snapshot_dir="$work/promisor-unchanged"
cp -R "$state_dir" "$snapshot_dir"
cd "$work/promisor/repo"
expect 0 'promisor missing before, forced push' '' \
  GIT_ALLOW_PROTOCOL=file BEFORE_SHA="$promisor_before" HEAD_SHA="$promisor_head" PUSH_FORCED=true
expect 1 'promisor missing before, ordinary push' 'BEFORE_SHA is unavailable for a nonforced push' \
  GIT_ALLOW_PROTOCOL=file BEFORE_SHA="$promisor_before" HEAD_SHA="$promisor_head" PUSH_FORCED=false

printf 'check-push-diff: %s cases passed\n' "$tests"
