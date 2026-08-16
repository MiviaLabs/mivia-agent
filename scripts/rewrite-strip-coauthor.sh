#!/usr/bin/env sh
# Rewrite all commits on master, removing disallowed Co-authored-by trailer
# lines (same email list as scripts/git-hooks/strip_coauthor.py). Mivia Agent
# (<noreply@mivia.app>) co-author lines are protected and stay.
set -e
cd "$(dirname "$0")/.."

echo "=== co-authored-by lines before rewrite ==="
git log master --format='%(trailers:key=Co-authored-by,valueonly)' | grep -v '^$' | sort | uniq -c | sort -rn

OLD_MASTER=$(git rev-parse master)
OLD_TREE=$(git rev-parse master^{tree})
echo "old master: $OLD_MASTER"
echo "old tree:   $OLD_TREE"

git update-ref refs/backup/master-pre-coauthor-strip "$OLD_MASTER"

# Set aside dirty tracked changes so filter-branch's clean-worktree check
# passes; restored afterwards even on failure.
stashed=0
if ! git diff --quiet || ! git diff --cached --quiet; then
  git stash push -q -m "temp-clean-during-coauthor-strip"
  stashed=1
fi
restore() {
  if [ "$stashed" = 1 ]; then git stash pop -q || echo "WARN: stash pop failed"; fi
}
trap restore EXIT

STRIP="$PWD/scripts/git-hooks/strip_coauthor.py"
git filter-branch --force --msg-filter "python3 $STRIP -" -- master

NEW_TREE=$(git rev-parse master^{tree})
echo "new tree:   $NEW_TREE"
echo "new master: $(git rev-parse master)"

echo "=== co-authored-by lines after rewrite ==="
git log master --format='%(trailers:key=Co-authored-by,valueonly)' | grep -v '^$' | sort | uniq -c | sort -rn

REMAINING=$(git log master --format='%(trailers:key=Co-authored-by,valueonly)' | grep -v '^$' | grep -v 'noreply@mivia.app' | wc -l)
echo "non-mivia co-author lines remaining: $REMAINING"
[ "$REMAINING" -eq 0 ] || {
  echo "ERROR: unexpected co-author lines remain; fix STRIP_EMAILS and retry" >&2
  exit 1
}
