#!/usr/bin/env python3
"""Validate that all test names referenced in .ai/invariants.md exist in the codebase.

Extracts backtick-quoted `Test*` names from the manifest markdown, extracts all
func Test names from Go test files, and fails if any reference is stale.
"""

import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
MANIFEST = REPO / ".ai" / "invariants.md"


def main() -> None:
    manifest_text = MANIFEST.read_text()
    # Extract backtick-quoted test names from markdown table cells
    refs = set(re.findall(r"`(Test\w+)`", manifest_text))
    if not refs:
        print("FAIL: no test references found in .ai/invariants.md")
        sys.exit(1)

    # Extract all func Test names from Go test files
    result = subprocess.run(
        ["rg", "-n", "^func Test", "-g", "*_test.go"],
        capture_output=True,
        text=True,
        cwd=REPO,
    )
    existing = set(re.findall(r"func (Test\w+)", result.stdout))
    missing = {t for t in refs if t not in existing}

    if missing:
        print(
            f"FAIL: {len(missing)} test(s) referenced in .ai/invariants.md "
            f"not found in codebase:"
        )
        for t in sorted(missing):
            print(f"  - {t}")
        print("Fix: rename or remove the stale entries in .ai/invariants.md")
        sys.exit(1)

    print(f"OK: all {len(refs)} referenced tests exist in codebase")


if __name__ == "__main__":
    main()
