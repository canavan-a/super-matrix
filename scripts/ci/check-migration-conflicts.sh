#!/usr/bin/env bash
#
# Fails when a branch adds a goose migration whose 5-digit number is already
# taken on main (or is duplicated among the branch's own added files).
#
# Goose migrations live in server/database/migrations/sql/ and are named
# NNNNN_description.sql with a sequential integer.
# Two branches each adding 00010_*.sql do not conflict in git (different
# filenames) but goose rejects the duplicate version at runtime. This catches
# it in CI.
#
# Usage: check-migration-conflicts.sh [BASE_REF]   (default: origin/main)

set -euo pipefail

BASE="${1:-origin/main}"
DIRS="server/database/migrations/sql"

if ! git rev-parse --verify --quiet "$BASE" >/dev/null; then
  echo "error: base ref '$BASE' not found (fetch it first)" >&2
  exit 2
fi

# 5-digit prefix from a migration path, or empty if the name is malformed.
prefix_of() {
  local base
  base="$(basename "$1")"
  if [[ "$base" =~ ^([0-9]{5})_.+\.sql$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
  fi
}

conflicts=0

for dir in $DIRS; do
  # Numbers already present on the base branch.
  declare -A existing=()
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    n="$(prefix_of "$f")"
    [ -n "$n" ] && existing["$n"]="$f"
  done < <(git ls-tree -r --name-only "$BASE" -- "$dir" | grep '\.sql$' || true)

  # Files added on this branch relative to the base.
  declare -A added=()
  while IFS= read -r f; do
    [ -n "$f" ] || continue

    n="$(prefix_of "$f")"
    if [ -z "$n" ]; then
      echo "CONFLICT: $f does not match NNNNN_description.sql" >&2
      conflicts=$((conflicts + 1))
      continue
    fi

    if [ -n "${existing[$n]:-}" ]; then
      echo "CONFLICT: $f reuses migration number $n already on ${BASE} (${existing[$n]})" >&2
      conflicts=$((conflicts + 1))
    fi

    if [ -n "${added[$n]:-}" ]; then
      echo "CONFLICT: $f and ${added[$n]} both use migration number $n" >&2
      conflicts=$((conflicts + 1))
    fi
    added["$n"]="$f"
  done < <(git diff --name-only --diff-filter=A "$BASE"...HEAD -- "$dir" | grep '\.sql$' || true)

  unset existing added
done

if [ "$conflicts" -gt 0 ]; then
  echo "" >&2
  echo "Found $conflicts migration number conflict(s). Renumber the new migration(s) to the next free number." >&2
  exit 1
fi

echo "No migration number conflicts."
