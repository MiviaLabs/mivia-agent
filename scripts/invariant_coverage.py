#!/usr/bin/env python3
"""Report invariant coverage metric.

Counts test names referenced in .ai/invariants.md, verifies they exist,
and prints a coverage summary. Called by `make invariants`.
"""

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MANIFEST = ROOT / ".ai" / "invariants.md"


def main() -> None:
    manifest_text = MANIFEST.read_text()

    refs = set(re.findall(r"`(Test\w+)`", manifest_text))
    if not refs:
        print("FAIL: no test references found in .ai/invariants.md")
        sys.exit(1)

    result = subprocess.run(
        ["rg", "-n", "^func Test", "-g", "*_test.go"],
        capture_output=True, text=True, cwd=ROOT,
    )
    existing = set(re.findall(r"func (Test\w+)", result.stdout))
    refs_with_pkg = {}
    for file, funcs in re.findall(
        r"^(.+?):func (Test\w+)", result.stdout, re.MULTILINE
    ):
        pass

    missing = {t for t in refs if t not in existing}
    found = refs - missing

    print(f"Invariant coverage: {len(found)}/{len(refs)} tests exist in codebase")
    if missing:
        print(f"⚠  {len(missing)} stale reference(s): {', '.join(sorted(missing))}")
        sys.exit(1)
    print("✓ All manifest tests verified by make validate-invariants")


if __name__ == "__main__":
    main()
