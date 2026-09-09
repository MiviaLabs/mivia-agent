#!/usr/bin/env python3
"""Report invariant coverage metric.

Counts test names referenced in .mivia/invariants.md, verifies they exist,
and prints a coverage summary. Called by `make invariants`.
"""

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MANIFEST = ROOT / ".mivia" / "invariants.md"


def main() -> None:
    manifest_text = MANIFEST.read_text()

    refs = set(re.findall(r"`(Test\w+)`", manifest_text))
    if not refs:
        print("FAIL: no test references found in .mivia/invariants.md")
        sys.exit(1)

    try:
        result = subprocess.run(
            # See validate_invariants.py: the "." path and stdin=DEVNULL stop
            # ripgrep searching stdin instead of the tree.
            ["rg", "-n", "^func Test", "-g", "*_test.go", "."],
            capture_output=True, text=True, cwd=ROOT,
            stdin=subprocess.DEVNULL,
        )
        existing = set(re.findall(r"func (Test\w+)", result.stdout))
    except (OSError, ValueError):
        existing = set()
    # rg is a fast path only: it may be absent (not every runner ships it), so
    # any non-positive result falls back to a stdlib walk instead of reporting
    # the tests as missing.
    #
    # This fallback used to blame "Homebrew's ripgrep on macOS" for the empty
    # result. That was a misdiagnosis: the call passed no search path, so
    # ripgrep searched stdin instead of the tree on every platform whose stdin
    # was not a tty. The fallback quietly produced the right answer and hid the
    # real bug, which in its other form blocks forever.
    if not existing:
        existing = set()
        skip = {".git", "testdata", "node_modules", "vendor"}
        for path in ROOT.rglob("*_test.go"):
            if skip & set(path.relative_to(ROOT).parts):
                continue
            existing.update(re.findall(r"(?m)^func (Test\w+)", path.read_text(encoding="utf-8")))

    missing = {t for t in refs if t not in existing}
    found = refs - missing

    print(f"Invariant coverage: {len(found)}/{len(refs)} tests exist in codebase")
    if missing:
        print(f"⚠  {len(missing)} stale reference(s): {', '.join(sorted(missing))}")
        sys.exit(1)
    print("✓ All manifest tests verified by make validate-invariants")


if __name__ == "__main__":
    main()
