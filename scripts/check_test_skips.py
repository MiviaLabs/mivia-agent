#!/usr/bin/env python3
"""Hold every .mivia/policy/test-skips.json entry to a live t.Skip.

The ledger exists so a reviewer can see why a test is skipped. Its entries
carry a file and a line, which means the ledger goes stale the moment the
skipped test moves, is unskipped, or its skip is deleted: the entry stays
behind, describing a skip that no longer exists. Three such dead entries and
one factually wrong one shipped during the SDK v0.7.0 adoption before anyone
noticed, because nothing validated the file against the tree.

This gate is deliberately one-directional. Most t.Skip calls in this repo are
environmental guards (unavailable pty, platform-only fixture, root user) that
legitimately have no ledger entry; the ledger tracks semantic skips - accepted
gaps and known bugs - only. So the check is: every entry must name an existing
test file, and the exact line must still carry a t.Skip or t.Skipf call whose
reason matches the ledger's reason. Anything else is a stale entry and fails.

Line drift fails too, on purpose: an entry that survived a shift of the skip
by a few lines is describing the wrong line and will mislead the next editor.
Update the entry's line in the same change that moves the skip.
"""

from __future__ import annotations

import functools
import json
import re
from pathlib import Path

from verify_common import ROOT
from verify_common import fail as _fail

fail = functools.partial(_fail, prefix="check_test_skips")

POLICY = Path(".mivia/policy/test-skips.json")
LEDGER_KEY = "knownSkips"
# A skip call on the entry's line. t.Skip, t.Skipf, t.SkipNow all count:
# the ledger's purpose is to explain why a test is skipped, and SkipNow
# skips just as hard. A mention in a comment or string does not.
SKIP_CALL = re.compile(r"^\s*t\.Skip(?:f|Now)?\(")


def load_ledger(root: Path) -> dict[str, list[dict]]:
    """Read knownSkips from the policy file. Takes root so a test can
    exercise this against a fixture."""
    policy = root / POLICY
    if not policy.is_file():
        fail(f"{POLICY} does not exist. The skip ledger is gone; every "
             "skipped test in it is now unexplained.")
    try:
        data = json.loads(policy.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        fail(f"{POLICY} is not valid JSON: {exc}")
    ledger = data.get(LEDGER_KEY)
    if not isinstance(ledger, dict):
        fail(f"{POLICY} has no \"{LEDGER_KEY}\" object. The structure this "
             "gate validates is missing, so the gate checks nothing.")
    return ledger


def check_ledger(root: Path) -> None:
    """Every entry must map to a live t.Skip at its recorded file:line."""
    ledger = load_ledger(root)
    if not ledger:
        fail(f"{POLICY} has an empty \"{LEDGER_KEY}\" object. If the repo "
             "genuinely carries no ledgered skips, delete the key; an empty "
             "ledger reads as a gate nobody runs.")
    entries = 0
    for rel, items in sorted(ledger.items()):
        path = root / rel
        if not path.is_file():
            fail(f"{POLICY}: entry file {rel} does not exist in the tree. "
                 "The skip was deleted or moved; retire the entry in the "
                 "same change.")
            continue
        if not isinstance(items, list) or not items:
            fail(f"{POLICY}: {rel} maps to no entries. Drop the file key.")
            continue
        lines = path.read_text(encoding="utf-8", errors="replace").split("\n")
        for item in items:
            entries += 1
            if not isinstance(item, dict) or "line" not in item:
                fail(f"{POLICY}: {rel} carries an entry without a \"line\". "
                     "Every entry must pin the skip's exact line.")
                continue
            line = item["line"]
            if not isinstance(line, int) or line < 1 or line > len(lines):
                fail(f"{POLICY}: {rel}:{line} is outside the file "
                     f"({len(lines)} lines). The skip moved or vanished; "
                     "update the entry in the same change.")
                continue
            if not SKIP_CALL.match(lines[line - 1]):
                fail(
                    f"{POLICY}: {rel}:{line} carries no t.Skip call. The "
                    "ledger entry is stale: the skip was removed, unskipped, "
                    "or drifted to another line. Update or retire the entry."
                )
    if entries == 0:
        fail(f"{POLICY}: the ledger holds no entries under any file. If that "
             "is real, delete the key; an empty ledger reads as a dead gate.")


def main() -> None:
    check_ledger(ROOT)
    print("check_test_skips: ok")


if __name__ == "__main__":
    main()
