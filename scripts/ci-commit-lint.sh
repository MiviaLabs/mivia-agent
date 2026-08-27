#!/usr/bin/env bash
# Lint every commit subject in a PR range against the repo commit-message policy.
# Skips Git-generated subjects the commit-msg hook also exempts (merge, revert,
# fixup!, squash!).
set -euo pipefail

BASE="${1:?base sha required}"
HEAD="${2:?head sha required}"

ROOT="$(git rev-parse --show-toplevel)"
CHECK="$ROOT/scripts/git-hooks/check-commit-subject"

# Historical commits that slipped past the commit-msg hook before v0.1.0 and
# now live in shared history. Rewriting shared/pushed history to fix them is
# riskier than the defect itself, so they are allowlisted by hash instead of
# force-pushed away. Do not add new entries for anything not already merged.
HISTORICAL_EXCEPTIONS="
b8dae435a0d98777b5774138c022774e6046d115
"

failed=0
while IFS= read -r sha; do
  case "$HISTORICAL_EXCEPTIONS" in
    *"$sha"*) continue ;;
  esac
  subject="$(git log -1 --format='%s' "$sha")"
  case "$subject" in
    Merge\ *|Revert\ *|fixup!\ *|squash!\ *) continue ;;
  esac
  # Strip a trailing GitHub squash-merge PR reference (e.g. " (#231)"). GitHub
  # appends this to the subject after the commit-msg hook already validated
  # the authored subject, so it must not count against maxSubjectLength here.
  checked_subject="$(echo "$subject" | sed -E 's/ \(#[0-9]+\)$//')"
  if ! python3 "$CHECK" "$checked_subject" >/dev/null 2>&1; then
    echo "commit-lint: invalid subject: $subject" >&2
    python3 "$CHECK" "$checked_subject" >&2 || true
    failed=1
  fi
done < <(git rev-list "$BASE..$HEAD")

if [ "$failed" -ne 0 ]; then
  echo "commit-lint: one or more commit subjects are invalid" >&2
  exit 1
fi
echo "commit-lint: all commit subjects valid"
