#!/usr/bin/env python3
"""Validate that all test names referenced in .mivia/invariants.md exist in the codebase
and do not contain unallowlisted self-disarming t.Skip calls.

Extracts backtick-quoted `Test*` names from the manifest markdown, extracts all
func Test names from Go test files, checks for missing references, and verifies
that invariant tests do not self-disarm with unallowlisted skips.
"""

import json
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
MANIFEST = REPO / ".mivia" / "invariants.md"
MAKEFILE = REPO / "Makefile"
POLICY_SKIPS_PATH = REPO / ".mivia" / "policy" / "invariant-skips.json"


def collect_test_names() -> set[str]:
    """Every `func Test*` name in the tree."""
    try:
        result = subprocess.run(
            ["rg", "-n", "^func Test", "-g", "*_test.go"],
            capture_output=True,
            text=True,
            cwd=REPO,
            check=False,
        )
        if result.returncode in (0, 1) and result.stdout.strip():
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
    """Fail on a duplicated invariant ID."""
    seen: dict[str, int] = {}
    dupes: list[str] = []
    in_definitions = True
    for lineno, line in enumerate(manifest_text.splitlines(), 1):
        heading = line.strip().lower()
        if heading.startswith("#"):
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


def load_invariant_skips_policy(policy_path: Path = POLICY_SKIPS_PATH) -> dict:
    if not policy_path.is_file():
        return {}
    try:
        return json.loads(policy_path.read_text(encoding="utf-8")).get("allowlistedSkips", {})
    except Exception as e:
        print(f"FAIL: invalid policy {policy_path}: {e}")
        sys.exit(1)


def check_invariant_skips(manifest_refs: set[str], repo: Path = REPO, policy_path: Path = POLICY_SKIPS_PATH) -> list[str]:
    """Finds all t.Skip calls in manifest-referenced invariant tests and fails on unallowlisted ones."""
    allowlisted = load_invariant_skips_policy(policy_path)
    violations = []

    skip_dirs = {".git", "testdata", "node_modules", "vendor"}
    for path in repo.rglob("*_test.go"):
        if skip_dirs & set(path.relative_to(repo).parts):
            continue
        content = path.read_text(encoding="utf-8")
        for m in re.finditer(r"(?m)^func (Test\w+)\s*\(", content):
            test_name = m.group(1)
            if test_name not in manifest_refs:
                continue

            start_pos = m.start()
            body_start = content.find("{", start_pos)
            if body_start == -1:
                continue
            next_func = content.find("\nfunc ", body_start)
            body = content[body_start:next_func] if next_func != -1 else content[body_start:]

            if re.search(r"\bt\.Skip(f|Now)?\(", body):
                if test_name not in allowlisted:
                    rel_path = str(path.relative_to(repo))
                    line_offset = content.count("\n", 0, start_pos) + 1
                    violations.append(f"{test_name} in {rel_path}:{line_offset}")

    return violations


def main() -> None:
    manifest_text = MANIFEST.read_text(encoding="utf-8")
    check_duplicate_ids(manifest_text)
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

    # Check for unallowlisted self-disarming skips in invariant tests
    skip_violations = check_invariant_skips(refs)
    if skip_violations:
        print(f"FAIL: {len(skip_violations)} manifest-referenced invariant test(s) contain unallowlisted self-disarming t.Skip:")
        for v in sorted(skip_violations):
            print(f"  - {v}")
        print("Fix: invariant tests must not self-disarm by skipping. If the skip is an accepted OS capability limitation, declare it in .mivia/policy/invariant-skips.json.")
        sys.exit(1)

    makefile_text = MAKEFILE.read_text(encoding="utf-8")
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

    print(f"OK: all {len(refs)} referenced tests exist, have no unallowlisted self-disarming skips, and are selected by make invariants")


if __name__ == "__main__":
    main()
