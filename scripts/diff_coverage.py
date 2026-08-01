#!/usr/bin/env python3
"""Diff-coverage gate: fail if changed Go lines are not exercised by any test.

Mechanizes the "diff coverage" step in verify-code-change/SKILL.md: a green
test suite proves nothing about lines it never executes. This script maps
git-diff added/modified lines in non-test .go files onto a coverage profile
instrumented across every package (-coverpkg=./...) and fails on any changed
statement line with a zero hit count, or on any changed file whose package
produced no coverage data at all (never linked into a tested binary).

Modes:
  --staged                staged changes vs HEAD (fast local loop)
  --base REF [--tip REF]  REF..TIP commit range (pre-push/CI; TIP defaults to HEAD)

Exit codes:
  0 = OK (no uncovered changed lines, or nothing in scope)
  1 = uncovered changed line(s) found
  2 = usage / environment error (no scope given, go missing, go test failed)

Policy reuse: .mivia/policy/go-structure.json excludeGlobs (generated/vendor).
"""
from __future__ import annotations

import argparse
import fnmatch
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HUNK_RE = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")
PROFILE_RE = re.compile(r"^(.+):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)$")


def repo_root() -> Path:
    r = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        print("diff_coverage: not inside a git repository", file=sys.stderr)
        sys.exit(2)
    return Path(r.stdout.strip())


def module_path(root: Path) -> str:
    gomod = root / "go.mod"
    if not gomod.is_file():
        print(f"diff_coverage: {gomod} not found", file=sys.stderr)
        sys.exit(2)
    for line in gomod.read_text(encoding="utf-8").splitlines():
        if line.startswith("module "):
            return line.split(None, 1)[1].strip()
    print("diff_coverage: could not read module path from go.mod", file=sys.stderr)
    sys.exit(2)


def load_exclude_globs(root: Path) -> list[str]:
    policy_path = root / ".mivia" / "policy" / "go-structure.json"
    if not policy_path.is_file():
        return []
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    return policy.get("excludeGlobs") or []


def changed_go_files(root: Path, diff_args: list[str]) -> list[str]:
    r = subprocess.run(
        ["git", "diff", *diff_args, "--name-only", "--diff-filter=ACMR", "-z", "--", "*.go"],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        print(f"diff_coverage: git diff failed: {r.stderr}", file=sys.stderr)
        sys.exit(2)
    return [f for f in r.stdout.strip("\0").split("\0") if f]


def changed_lines(root: Path, diff_args: list[str], path: str) -> set[int]:
    r = subprocess.run(
        ["git", "diff", *diff_args, "-U0", "--", path],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        print(f"diff_coverage: git diff -U0 failed for {path}: {r.stderr}", file=sys.stderr)
        sys.exit(2)
    lines: set[int] = set()
    for line in r.stdout.splitlines():
        m = HUNK_RE.match(line)
        if not m:
            continue
        start = int(m.group(1))
        count = int(m.group(2)) if m.group(2) is not None else 1
        if count == 0:
            continue  # pure deletion; nothing added in the new file
        lines.update(range(start, start + count))
    return lines


def run_coverage_profile(root: Path) -> Path:
    if shutil.which("go") is None:
        print("diff_coverage: go not found on PATH", file=sys.stderr)
        sys.exit(2)
    fd, path = tempfile.mkstemp(prefix="mivia-diffcov-", suffix=".out")
    os.close(fd)
    profile = Path(path)
    proc = subprocess.run(
        ["go", "test", "./...", "-coverpkg=./...", f"-coverprofile={profile}"],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if proc.returncode != 0:
        print(proc.stdout, file=sys.stderr)
        print(proc.stderr, file=sys.stderr)
        print("diff_coverage: go test failed; fix failing tests before checking diff coverage", file=sys.stderr)
        profile.unlink(missing_ok=True)
        sys.exit(2)
    return profile


def parse_profile(profile: Path, module: str) -> dict[str, list[tuple[int, int, int]]]:
    blocks: dict[str, list[tuple[int, int, int]]] = {}
    prefix = module + "/"
    with profile.open(encoding="utf-8") as fh:
        next(fh, None)  # mode: ...
        for line in fh:
            m = PROFILE_RE.match(line.strip())
            if not m:
                continue
            file_path, start, end, count = m.group(1), int(m.group(2)), int(m.group(3)), int(m.group(4))
            if file_path.startswith(prefix):
                file_path = file_path[len(prefix):]
            blocks.setdefault(file_path, []).append((start, end, count))
    return blocks


def uncovered_for_file(file_lines: set[int], blocks: list[tuple[int, int, int]]) -> tuple[list[int], int]:
    """Return (uncovered changed lines, count of changed lines that are actual statements)."""
    uncovered = []
    checkable = 0
    for line in sorted(file_lines):
        matched = False
        covered = False
        for start, end, count in blocks:
            if start <= line <= end:
                matched = True
                if count > 0:
                    covered = True
                    break
        if matched:
            checkable += 1
            if not covered:
                uncovered.append(line)
    return uncovered, checkable


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--staged", action="store_true", help="check staged changes vs HEAD")
    parser.add_argument("--base", help="base ref; checks --base..--tip")
    parser.add_argument("--tip", default="HEAD", help="tip ref for --base mode (default HEAD)")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)

    if args.staged and args.base:
        print("diff_coverage: pass either --staged or --base, not both", file=sys.stderr)
        return 2
    if args.staged:
        diff_args = ["--cached"]
    elif args.base:
        diff_args = [args.base, args.tip]
    else:
        print("diff_coverage: specify --staged or --base REF", file=sys.stderr)
        return 2

    root = repo_root()
    excludes = load_exclude_globs(root)
    files = [
        f for f in changed_go_files(root, diff_args)
        if not f.endswith("_test.go") and not any(fnmatch.fnmatch(f, g) for g in excludes)
    ]
    if not files:
        print("diff_coverage: no changed non-test Go files in scope; skipping")
        return 0

    per_file_lines = {f: lines for f in files if (lines := changed_lines(root, diff_args, f))}
    if not per_file_lines:
        print("diff_coverage: no changed non-test Go lines in scope; skipping")
        return 0

    profile = run_coverage_profile(root)
    try:
        blocks_by_file = parse_profile(profile, module_path(root))
    finally:
        profile.unlink(missing_ok=True)

    total_checked = 0
    total_uncovered = 0
    findings: list[str] = []
    for f, lines in sorted(per_file_lines.items()):
        if f not in blocks_by_file:
            # Package never linked into any tested binary: every changed line
            # in it is unproven, not merely blank/comment.
            for line in sorted(lines):
                findings.append(f"{f}:{line} (package produced no coverage data)")
                total_uncovered += 1
                total_checked += 1
            continue
        uncovered, checkable = uncovered_for_file(lines, blocks_by_file[f])
        total_checked += checkable
        total_uncovered += len(uncovered)
        for line in uncovered:
            findings.append(f"{f}:{line}")

    if total_uncovered:
        print(f"diff_coverage: {total_uncovered}/{total_checked} changed statement line(s) not covered by any test:")
        for finding in findings:
            print(f"  {finding}")
        return 1

    print(f"diff_coverage: {total_checked}/{total_checked} changed statement line(s) covered")
    return 0


if __name__ == "__main__":
    sys.exit(main())
