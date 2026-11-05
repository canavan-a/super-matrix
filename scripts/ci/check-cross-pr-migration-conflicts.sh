#!/usr/bin/env bash
#
# Advisory check: warns (via a PR comment) when this PR adds a goose migration
# whose 5-digit number is also being added by another *open* PR in the same
# directory. Git sees no conflict (different filenames) and neither does
# check-migration-conflicts.sh (which only looks at origin/main), but whichever
# PR merges second will make goose reject the duplicate version at runtime.
#
# This never fails the build - it only posts / refreshes a single marker
# comment on the PR, flipping it green once the conflict clears.
#
# Usage: check-cross-pr-migration-conflicts.sh REPO PR_NUMBER
#   REPO       owner/name  (e.g. canavan-a/super-matrix)
#   PR_NUMBER  the current pull request number
#
# Requires: gh (authenticated via GH_TOKEN), git with origin/main fetched.

set -euo pipefail

REPO="${1:?usage: check-cross-pr-migration-conflicts.sh REPO PR_NUMBER}"
PR_NUMBER="${2:?usage: check-cross-pr-migration-conflicts.sh REPO PR_NUMBER}"

DIRS="server/database/migrations/sql"
MARKER="<!-- cross-pr-migration-conflict -->"

# 5-digit prefix from a migration path, or empty if the name is malformed.
prefix_of() {
  local base
  base="$(basename "$1")"
  if [[ "$base" =~ ^([0-9]{5})_.+\.sql$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
  fi
}

# short dir label: server/database/migrations/sql -> sql
label_of() { basename "$1"; }

if ! git rev-parse --verify --quiet origin/main >/dev/null; then
  echo "error: origin/main not found (fetch it first)" >&2
  exit 2
fi

# --- numbers added by this PR, keyed as "<label>/<number>" -> filename ---------
declare -A ours=()
have_ours=0
for dir in $DIRS; do
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    n="$(prefix_of "$f")"
    [ -n "$n" ] || continue
    ours["$(label_of "$dir")/$n"]="$f"
    have_ours=1
  done < <(git diff --name-only --diff-filter=A origin/main...HEAD -- "$dir" | grep '\.sql$' || true)
done

if [ "$have_ours" -eq 0 ]; then
  echo "This PR adds no goose migrations - nothing to check."
  exit 0
fi

# --- scan other open PRs -----------------------------------------------------
conflict_rows=()   # "<label>/<number>\t<ourfile>\t<pr#>\t<pr title>"

while IFS=$'\t' read -r num title; do
  [ -n "$num" ] || continue
  [ "$num" != "$PR_NUMBER" ] || continue

  while IFS= read -r path; do
    [ -n "$path" ] || continue
    case "$path" in
      server/database/migrations/sql/*.sql) ;;
      *) continue ;;
    esac
    n="$(prefix_of "$path")"
    [ -n "$n" ] || continue
    key="$(basename "$(dirname "$path")")/$n"
    if [ -n "${ours[$key]:-}" ]; then
      conflict_rows+=("$key"$'\t'"${ours[$key]}"$'\t'"$num"$'\t'"$title")
    fi
  done < <(gh pr view "$num" --repo "$REPO" --json files --jq '.files[].path' 2>/dev/null || true)
done < <(gh pr list --repo "$REPO" --state open --limit 200 \
           --json number,title --jq '.[] | "\(.number)\t\(.title)"')

# --- build comment body ----------------------------------------------------
body_file="$(mktemp)"
trap 'rm -f "$body_file"' EXIT

{
  printf '%s\n\n' "$MARKER"
  if [ "${#conflict_rows[@]}" -eq 0 ]; then
    printf '✅ No goose migration number conflicts with other open PRs.\n'
  else
    printf '## 🔴 Migration number conflict with another open PR\n\n'
    printf 'Another open PR is adding a goose migration with the same number in the same directory. Whichever merges second will break goose at runtime.\n\n'
    for row in "${conflict_rows[@]}"; do
      IFS=$'\t' read -r key ourfile pr title <<<"$row"
      printf -- '- 🔴 `%s` (`%s`) — also added by #%s "%s"\n' "$key" "$ourfile" "$pr" "$title"
    done
    printf '\nRenumber the new migration(s) to the next free number.\n'
  fi
} >"$body_file"

# --- post / update the marker comment ------------------------------------
existing_id="$(gh api "repos/$REPO/issues/$PR_NUMBER/comments" --paginate \
  --jq ".[] | select(.body | contains(\"$MARKER\")) | .id" 2>/dev/null | head -n1 || true)"

if [ -n "$existing_id" ]; then
  gh api --method PATCH "repos/$REPO/issues/comments/$existing_id" \
    -f body="$(cat "$body_file")" >/dev/null
  echo "Updated existing conflict comment ($existing_id)."
else
  gh pr comment "$PR_NUMBER" --repo "$REPO" --body-file "$body_file"
  echo "Posted new conflict comment."
fi

if [ "${#conflict_rows[@]}" -gt 0 ]; then
  echo "Found ${#conflict_rows[@]} cross-PR migration number conflict(s) (advisory only)."
fi
