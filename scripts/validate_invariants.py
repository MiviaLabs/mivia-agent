#!/usr/bin/env python3
"""Validate that all test names referenced in .mivia/invariants.md exist in the codebase
and do not contain unallowlisted self-disarming t.Skip calls.

Modes:
  (no args)   validate manifest integrity, test presence, and zero unallowlisted skips
  --regex     print regex selecting all manifest invariant tests
  --run       run all manifest invariant tests via go test
"""
from __future__ import annotations

import argparse
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
            # "." and stdin=DEVNULL are both load-bearing. With no path
            # argument ripgrep searches STDIN whenever stdin is not a tty -
            # which is every non-interactive context: CI, a git hook, an agent.
            # That makes this call block forever on a stdin that never reaches
            # EOF, or return zero matches on one that does. Neither is a search
            # of the repository.
            ["rg", "-n", "^func Test", "-g", "*_test.go", "."],
            capture_output=True,
            text=True,
            cwd=REPO,
            check=False,
            stdin=subprocess.DEVNULL,
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


def load_invariant_skips_policy(policy_path: Path = POLICY_SKIPS_PATH, repo: Path = REPO, diff_args: list[str] | None = None) -> dict:
    if not policy_path.is_file():
        return {}

    raw_text = ""
    is_modified_in_diff = False
    diff_scope = diff_args if diff_args is not None else ["--cached"]
    r = subprocess.run(
        ["git", "diff", *diff_scope, "--name-only", "--", str(policy_path.relative_to(repo) if policy_path.is_relative_to(repo) else policy_path)],
        cwd=repo, capture_output=True, text=True, check=False,
    )
    if r.returncode == 0 and r.stdout.strip():
        is_modified_in_diff = True
        base_ref = "HEAD"
        if diff_args is not None:
            if len(diff_args) >= 2 and not diff_args[0].startswith("-"):
                base_ref = diff_args[0]
            elif len(diff_args) == 1 and ".." in diff_args[0]:
                base_ref = diff_args[0].split("..")[0]
            elif len(diff_args) == 1 and not diff_args[0].startswith("-"):
                base_ref = diff_args[0]

        rel_policy = str(policy_path.relative_to(repo) if policy_path.is_relative_to(repo) else ".mivia/policy/invariant-skips.json")
        show_res = subprocess.run(
            ["git", "show", f"{base_ref}:{rel_policy}"],
            cwd=repo, capture_output=True, text=True, check=False,
        )
        print(
            "validate_invariants: .mivia/policy/invariant-skips.json is modified in this diff; "
            "evaluating skips against base policy to prevent same-commit bypass",
            file=sys.stderr,
        )
        if show_res.returncode == 0 and show_res.stdout.strip():
            raw_text = show_res.stdout
        else:
            # File did not exist at base ref: fail closed with zero allowlisted skips
            return {}

    if not is_modified_in_diff:
        try:
            raw_text = policy_path.read_text(encoding="utf-8")
        except Exception as e:
            print(f"FAIL: invalid policy {policy_path}: {e}")
            sys.exit(1)

    try:
        return json.loads(raw_text).get("allowlistedSkips", {})
    except Exception as e:
        print(f"FAIL: invalid policy {policy_path}: {e}")
        sys.exit(1)


def check_invariant_skips(manifest_refs: set[str], repo: Path = REPO, policy_path: Path = POLICY_SKIPS_PATH, diff_args: list[str] | None = None) -> list[str]:
    """Finds all t.Skip calls in manifest-referenced invariant tests and fails on unallowlisted ones."""
    allowlisted = load_invariant_skips_policy(policy_path, repo, diff_args)
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


def build_invariant_regex(manifest_text: str) -> str:
    refs = sorted(set(re.findall(r"`(Test\w+)`", manifest_text)))
    if not refs:
        return "Test$^"
    return "^(" + "|".join(refs) + ")$"


def main() -> None:
    parser = argparse.ArgumentParser(description="Validate and run invariant tests.")
    parser.add_argument("--regex", action="store_true", help="Output regex selecting all manifest invariant tests")
    parser.add_argument("--run", action="store_true", help="Run all manifest invariant tests via go test")
    args = parser.parse_args()

    manifest_text = MANIFEST.read_text(encoding="utf-8")
    if args.regex:
        print(build_invariant_regex(manifest_text))
        return

    if args.run:
        regex = build_invariant_regex(manifest_text)
        cmd = ["go", "test", "-run", regex, "./...", "-count=1", "-timeout=180s"]
        res = subprocess.run(cmd, cwd=REPO)
        sys.exit(res.returncode)

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

    print(f"OK: all {len(refs)} referenced tests exist and have no unallowlisted self-disarming skips")


if __name__ == "__main__":
    main()
