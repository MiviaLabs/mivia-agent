#!/usr/bin/env bash
# Lint every commit subject in a PR range against the repo commit-message policy.
# Skips Git-generated subjects the commit-msg hook also exempts (merge, revert,
# fixup!, squash!).
set -euo pipefail

BASE="${1:?base sha required}"
HEAD="${2:?head sha required}"

ROOT="$(git rev-parse --show-toplevel)"
CHECK="$ROOT/scripts/git-hooks/check-commit-subject"

failed=0
while IFS= read -r sha; do
  subject="$(git log -1 --format='%s' "$sha")"
  case "$subject" in
    Merge\ *|Revert\ *|fixup!\ *|squash!\ *) continue ;;
  esac
  if ! python3 "$CHECK" "$subject" >/dev/null 2>&1; then
    echo "commit-lint: invalid subject: $subject" >&2
    python3 "$CHECK" "$subject" >&2 || true
    failed=1
  fi
done < <(git rev-list "$BASE..$HEAD")

if [ "$failed" -ne 0 ]; then
  echo "commit-lint: one or more commit subjects are invalid" >&2
  exit 1
fi
echo "commit-lint: all commit subjects valid"
