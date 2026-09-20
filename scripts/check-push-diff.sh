#!/usr/bin/env bash
set -euo pipefail

: "${BEFORE_SHA:?BEFORE_SHA required}"
: "${HEAD_SHA:?HEAD_SHA required}"
: "${PUSH_FORCED:?PUSH_FORCED required}"

# GitHub supplies full SHA-1 object IDs, not revision expressions or options.
if [[ ! "$BEFORE_SHA" =~ ^[0-9a-f]{40}$ || ! "$HEAD_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'BEFORE_SHA and HEAD_SHA must be full GitHub commit IDs\n' >&2
  exit 1
fi
case "$PUSH_FORCED" in
  true|false) ;;
  *) printf 'PUSH_FORCED must be true or false\n' >&2; exit 1 ;;
esac

# Object inspection must not fetch missing objects from a partial clone.
export GIT_NO_LAZY_FETCH=1
head_type="$(git cat-file -t "$HEAD_SHA")"
if [[ "$head_type" != commit ]]; then
  printf 'HEAD_SHA must identify a commit object\n' >&2
  exit 1
fi

# Batch inspection distinguishes a missing object from a Git error or a
# present noncommit object; neither of the latter permits the fallback. Include
# diagnostics because corrupt objects can report "missing" with exit status 0.
before_type="$(printf '%s\n' "$BEFORE_SHA" | git cat-file --batch-check='%(objecttype)' 2>&1)"
case "$before_type" in
  commit)
    git diff --check "$BEFORE_SHA..$HEAD_SHA"
    ;;
  "$BEFORE_SHA missing")
    if [[ "$PUSH_FORCED" != true ]]; then
      printf 'BEFORE_SHA is unavailable for a nonforced push\n' >&2
      exit 1
    fi
    empty_tree="$(git hash-object -t tree --stdin </dev/null)"
    git diff --check "$empty_tree" "$HEAD_SHA"
    ;;
  *)
    printf 'BEFORE_SHA must identify a commit object\n' >&2
    exit 1
    ;;
esac
