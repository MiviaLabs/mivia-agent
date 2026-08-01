#!/usr/bin/env python3
"""Validate that all test names referenced in .mivia/invariants.md exist in the codebase.

Extracts backtick-quoted `Test*` names from the manifest markdown, extracts all
func Test names from Go test files, and fails if any reference is stale.
"""

import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
MANIFEST = REPO / ".mivia" / "invariants.md"
MAKEFILE = REPO / "Makefile"


def collect_test_names() -> set[str]:
    """Every `func Test*` name in the tree.

    Prefers ripgrep, but falls back to a stdlib walk. This runs inside
    `make verify`, and `rg` is not one of the tool's declared local
    requirements (python3, plus go/gofmt and semgrep) - so the gate must not
    depend on it being installed.
    """
    try:
        result = subprocess.run(
            ["rg", "-n", "^func Test", "-g", "*_test.go"],
            capture_output=True,
            text=True,
            cwd=REPO,
            check=False,
        )
        if result.returncode in (0, 1):
            return set(re.findall(r"func (Test\w+)", result.stdout))
    except (OSError, ValueError):
        pass

    names: set[str] = set()
    skip = {".git", "testdata", "node_modules", "vendor"}
    for path in REPO.rglob("*_test.go"):
        if skip & set(path.relative_to(REPO).parts):
            continue
        names.update(re.findall(r"(?m)^func (Test\w+)", path.read_text(encoding="utf-8")))
    return names


def check_duplicate_ids(manifest_text: str) -> None:
    """Fail on a duplicated invariant ID.

    IDs are allocated by hand at landing time, lowest free. Nothing else parses
    them, so two plans claiming the same number silently produce one row that
    overwrites the other's meaning.

    Only the ID column of a definition row counts. `.mivia/invariants.md` also
    holds cross-reference tables (e.g. "Liveness Gap Notes") that key rows by an
    ID already defined above, and IDs are cited inline inside descriptions --
    counting either as a definition produces false duplicates.
    """
    seen: dict[str, int] = {}
    dupes: list[str] = []
    in_definitions = True
    for lineno, line in enumerate(manifest_text.splitlines(), 1):
        heading = line.strip().lower()
        if heading.startswith("#"):
            # Cross-reference tables restate IDs defined elsewhere.
            in_definitions = "gap" not in heading and "note" not in heading
            continue
        if not in_definitions:
            continue
        match = re.match(r"\|\s*(INV-[A-Z]+-\d+)\s*\|", line)
        if not match:
            continue
        inv_id = match.group(1)
        if inv_id in seen:
            dupes.append(f"  - {inv_id}: line {seen[inv_id]} and line {lineno}")
        else:
            seen[inv_id] = lineno

    if dupes:
        print("FAIL: duplicate invariant ID(s) in .mivia/invariants.md:")
        for d in dupes:
            print(d)
        print("Fix: allocate the lowest free ID above the current maximum per prefix")
        sys.exit(1)
    return None


def main() -> None:
    manifest_text = MANIFEST.read_text()
    check_duplicate_ids(manifest_text)
    # Extract backtick-quoted test names from markdown table cells
    refs = set(re.findall(r"`(Test\w+)`", manifest_text))
    if not refs:
        print("FAIL: no test references found in .mivia/invariants.md")
        sys.exit(1)

    existing = collect_test_names()
    missing = {t for t in refs if t not in existing}

    if missing:
        print(
            f"FAIL: {len(missing)} test(s) referenced in .mivia/invariants.md "
            f"not found in codebase:"
        )
        for t in sorted(missing):
            print(f"  - {t}")
        print("Fix: rename or remove the stale entries in .mivia/invariants.md")
        sys.exit(1)

    makefile_text = MAKEFILE.read_text()
    match = re.search(r"^invariants:.*?^-?\t@go test -run '([^']+)'", makefile_text, re.MULTILINE | re.DOTALL)
    if not match:
        print("FAIL: could not find the invariants go test regex in Makefile")
        sys.exit(1)
    invariant_regex = re.compile(match.group(1))
    skipped = {t for t in refs if not invariant_regex.search(t)}
    if skipped:
        print("FAIL: invariant test(s) are not selected by Makefile invariants regex:")
        for test in sorted(skipped):
            print(f"  - {test}")
        sys.exit(1)

    print(f"OK: all {len(refs)} referenced tests exist and are selected by make invariants")


if __name__ == "__main__":
    main()
