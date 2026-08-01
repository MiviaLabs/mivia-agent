#!/usr/bin/env python3
"""Diff-coverage gate: fail if changed Go lines are not exercised by any test.

Mechanizes the "diff coverage" step in verify-code-change/SKILL.md: a green
test suite proves nothing about lines it never executes. This script maps
git-diff added/modified lines in non-test .go files onto a coverage profile
instrumented across every package (-coverpkg=./...) and fails on any changed
statement line with a zero hit count, or on any changed file whose package
produced no coverage data at all (never linked into a tested binary). A changed
file that contributes no statements at all (declarations only) inside a package
that IS covered has nothing to execute and is not reported.

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


def tip_file_text(root: Path, diff_args: list[str], path: str) -> str | None:
    """Return the content of path as of the revision the diff compared against.

    The line numbers in a diff hunk index that revision, so any per-line
    judgement has to read the same bytes. Falls back to the working tree (and
    then to None) when the revision has no such blob.
    """
    spec = f":{path}" if diff_args[0] == "--cached" else f"{diff_args[-1]}:{path}"
    r = subprocess.run(
        ["git", "show", spec], cwd=root, capture_output=True, text=True, check=False,
    )
    if r.returncode == 0:
        return r.stdout
    worktree = root / path
    if worktree.is_file():
        return worktree.read_text(encoding="utf-8", errors="replace")
    return None


def non_executable_lines(text: str) -> set[int]:
    """Line numbers in text that cannot carry a statement: blank lines, whole-
    line // comments, and lines wholly inside a /* */ comment.

    A single left-to-right pass tracks whether a /* */ region is open, so a
    line in the middle of a commented-out block is recognised as such. String
    literals are not parsed: a `/*` inside a string would over-report, which
    costs a line of scrutiny rather than hiding an uncovered statement, and the
    statement's own lines are reported either way.
    """
    skip: set[int] = set()
    open_block = False
    for number, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        started_open = open_block
        has_code = False
        idx = 0
        while idx < len(line):
            if open_block:
                close = line.find("*/", idx)
                if close < 0:
                    idx = len(line)
                    break
                open_block = False
                idx = close + 2
                continue
            if line.startswith("//", idx):
                break
            if line.startswith("/*", idx):
                open_block = True
                idx += 2
                continue
            if not line[idx].isspace():
                has_code = True
            idx += 1
        if has_code:
            continue
        if line == "" or line.startswith("//") or started_open or open_block:
            skip.add(number)
    return skip


def executable_lines(text: str | None, lines: set[int]) -> set[int]:
    """Drop changed lines that cannot carry a statement.

    A coverage block spans from a statement's first line to its last, so blank
    lines and whole-line comments INSIDE a multi-line statement fall within a
    block and count as "checkable". When that statement is uncovered the gate
    then names a COMMENT as an uncovered line, and a comment-only edit inside
    untested code fails a gate that no test can satisfy. Nothing is hidden:
    every real statement in the same block carries its own line numbers, and
    those are still reported.
    """
    if text is None:
        return lines
    return lines - non_executable_lines(text)


def run_coverage_profile(root: Path) -> Path:
    if shutil.which("go") is None:
        print("diff_coverage: go not found on PATH", file=sys.stderr)
        sys.exit(2)
    fd, path = tempfile.mkstemp(prefix="mivia-diffcov-", suffix=".out")
    os.close(fd)
    profile = Path(path)
    # -count=1 is load-bearing, not hygiene. `go test` caches test RESULTS,
    # coverage output included, and a cached result is replayed with the block
    # coordinates of the source it was recorded against. After an edit shifts
    # line numbers, a replayed entry lands stale 0-count blocks on top of the
    # live ones: lines that no longer hold statements (comments, blank lines)
    # look "checkable and uncovered", and the gate reports phantom failures
    # that no test can fix. Re-running every test is the price of a profile
    # that describes the tree as it is now.
    proc = subprocess.run(
        ["go", "test", "./...", "-count=1", "-coverpkg=./...", f"-coverprofile={profile}"],
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

    per_file_lines = {}
    for f in files:
        lines = executable_lines(tip_file_text(root, diff_args, f), changed_lines(root, diff_args, f))
        if lines:
            per_file_lines[f] = lines
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
    covered_packages = {str(Path(f).parent) for f in blocks_by_file}
    no_statements: list[str] = []
    for f, lines in sorted(per_file_lines.items()):
        if f not in blocks_by_file:
            if str(Path(f).parent) in covered_packages:
                # The package IS linked into a tested binary, so the profile is
                # real; this file simply contributed no blocks. That means it
                # holds no statements in THIS build - declarations only, or a
                # build constraint excluded it on this platform. Neither can be
                # executed by any test, so the file is recorded as unchecked
                # rather than failed: flagging it makes an edit to a
                # declarations file unfixable.
                no_statements.append(f)
                continue
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

    # Say what was not checked. A gate that silently narrows its own scope
    # reads as "all clear" when it means "I did not look".
    for f in no_statements:
        print(f"diff_coverage: {f} contributed no statements to this build (declarations only, "
              f"or excluded by a build constraint); nothing to cover")

    if total_uncovered:
        print(f"diff_coverage: {total_uncovered}/{total_checked} changed statement line(s) not covered by any test:")
        for finding in findings:
            print(f"  {finding}")
        return 1

    print(f"diff_coverage: {total_checked}/{total_checked} changed statement line(s) covered")
    return 0


if __name__ == "__main__":
    sys.exit(main())
