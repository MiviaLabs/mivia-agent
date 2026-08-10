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
            ["rg", "-n", "^func Test", "-g", "*_test.go"],
            capture_output=True, text=True, cwd=ROOT,
        )
        existing = set(re.findall(r"func (Test\w+)", result.stdout))
    except (OSError, ValueError):
        existing = set()
    # rg is a fast path only: it may be absent (not every runner ships it) or
    # return zero matches for a tree full of tests (Homebrew's ripgrep on
    # macOS), so any non-positive result falls back to a stdlib walk instead
    # of reporting the tests as missing.
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
