#!/usr/bin/env python3
"""Diff-coverage gate: fail if changed Go lines are not exercised by any test.

Mechanizes the "diff coverage" step in verify-code-change/SKILL.md: a green
test suite proves nothing about lines it never executes. This script maps
git-diff added/modified lines in non-test .go files onto a coverage profile
instrumented across every package (-coverpkg=./...) and fails on any changed
statement line with a zero hit count, or on any changed file whose package
produced no coverage data at all (never linked into a tested binary). A changed
file that contributes no statements at all (declarations only) has nothing to
execute and is not reported: either the package IS covered and the file simply
holds no blocks, or Go's own instrumenter finds no statement in the file.

Modes:
  --staged                staged changes vs HEAD (fast local loop)
  --base REF [--tip REF]  REF..TIP commit range (pre-push/CI; TIP defaults to HEAD)

Exit codes:
  0 = OK (no uncovered changed lines, or nothing in scope)
  1 = uncovered changed line(s) found
  2 = usage / environment error (no scope given, go missing, go test failed)

Policy reuse: .mivia/policy/go-structure.json excludeGlobs (generated/vendor; internal/legacytui
is excluded there too because that package is scheduled for deletion at the end of the UI
migration - covering its lines would be waste, and the glob removes it from this gate's scope
as well as the structure gate's).
Per-line accepted residue: .mivia/policy/diff-coverage.json knownUncovered maps exact line
numbers to a reason; the gate prints every accepted line but does not fail on it.
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


def load_known_uncovered(root: Path) -> dict[str, tuple[set[int], str]]:
    """Per-line accepted residue from .mivia/policy/diff-coverage.json.

    Maps file path -> (line numbers, reason). Lines listed there are
    provably unreachable or not unit-testable; the gate still prints every
    accepted line so the residue stays reviewable, but does not fail on it.
    """
    policy_path = root / ".mivia" / "policy" / "diff-coverage.json"
    if not policy_path.is_file():
        return {}
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
    out: dict[str, tuple[set[int], str]] = {}
    for path, entry in (policy.get("knownUncovered") or {}).items():
        lines = set(int(n) for n in entry.get("lines", []))
        reason = str(entry.get("reason", "")).strip()
        if lines and reason:
            out[path] = (lines, reason)
    return out


def changed_go_files(root: Path, diff_args: list[str]) -> list[str]:
    r = subprocess.run(
        ["git", "diff", *diff_args, "--name-only", "--diff-filter=ACMR", "-z", "--", "*.go"],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        print(f"diff_coverage: git diff failed: {r.stderr}", file=sys.stderr)
        sys.exit(2)
    return [f for f in r.stdout.strip("\0").split("\0") if f]


# Pure renames (R100, similarity = 100%) move production lines without
# changing them. The coverage gate must not treat a rename like a fresh
# change, because the same lines are still exercised by the same tests
# in the moved package. rename_pairs() below maps each rename
# destination back to its source so the per-file line-diff is computed
# against the source instead of the merged base, which already excludes
# pure moves from the in-scope set without exempting any content edits
# inside the move.


def rename_pairs(root: Path, diff_args: list[str]) -> dict[str, str]:
    """Map destination path -> source path for every rename in the diff.

    The coverage gate computes "lines added in the destination vs the
    merged base" by default, but for a renamed file most of those lines
    already existed at the source. The right comparison is "lines added
    in the destination vs the source as-of-base", which is exactly the
    renamed-file hunk git reports when invoked with -M.

    Returns an empty dict when no rename detection ran; callers fall
    back to the default (whole-file) coverage check for those paths.

    With --name-status -z git emits one NUL-terminated record per field,
    not per logical entry: a rename is three records in a row
    (status+similarity, source path, destination path). We walk the
    records and pull the next two fields when the status starts with R.
    """
    r = subprocess.run(
        [
            "git", "diff", *diff_args, "-M",
            "--name-status", "-z", "--", "*.go",
        ],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        return {}
    fields = [f for f in r.stdout.split("\x00") if f]
    pairs: dict[str, str] = {}
    i = 0
    while i < len(fields):
        status = fields[i]
        if status.startswith("R") and i + 2 < len(fields):
            pairs[fields[i + 2]] = fields[i + 1]
            i += 3
        else:
            i += 1
    return pairs


def changed_lines(
    root: Path,
    diff_args: list[str],
    path: str,
    rename_source: str | None = None,
) -> set[int]:
    # For renamed files, diff against the source as-of-base so the only
    # lines we report are the ones that were actually edited in the move.
    # `git diff base src dst` produces hunks relative to dst.
    if rename_source:
        diff_path = (rename_source, path)
    else:
        diff_path = (path,)
    r = subprocess.run(
        ["git", "diff", *diff_args, "-U0", "--", *diff_path],
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
    line // comments, lines wholly inside a /* */ comment, and switch labels
    (`case X:` / `default:` in a Go switch). Switch labels are not executable:
    a `case "x":` line has no statement of its own; the statement that
    follows the label is the one that runs when the case is selected. Without
    filtering, a renumbered switch statement in a renamed file reports every
    case label as a fresh uncovered addition and the gate reports phantom
    violations that no test can satisfy.

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
            # Skip Go switch / select labels. They have code text but no
            # statement of their own; the next non-label line is the
            # statement that actually executes.
            if re.match(r"(case\s+[^\s:]+(\s*,\s*[^\s:]+)*\s*:|default\s*:|case\s+[^\n]*:\s*$)", line):
                skip.add(number)
                continue
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


def file_has_statements(root: Path, path: str, text: str | None) -> bool:
    """Report whether Go finds a single executable statement in text.

    A profile can prove that a package was linked only if the package holds at
    least one statement. A package of nothing but const, var, type and
    interface declarations - `ports`, a defaults table - emits no counters, so
    it is absent from every profile even when its own tests run. Reading that
    absence as "never linked" fails the package on lines that no test can ever
    execute, which is a gate no change can satisfy.

    The verdict comes from `go tool cover`, the same instrumenter that writes
    the profile, so "statement" means here exactly what it means there. This
    does not widen the exemption: one statement anywhere in the file makes the
    file checkable again, and a never-linked package that holds real code still
    fails. Any error answers True, which keeps the gate fail-closed.
    """
    if text is None or shutil.which("go") is None:
        return True
    with tempfile.TemporaryDirectory(prefix="mivia-diffcov-stmt-") as tmp:
        probe = Path(tmp) / Path(path).name
        probe.write_text(text, encoding="utf-8")
        proc = subprocess.run(
            ["go", "tool", "cover", "-mode=set", "-var=miviaDiffCovProbe", str(probe)],
            cwd=root, capture_output=True, text=True, check=False,
        )
    if proc.returncode != 0:
        return True
    return "miviaDiffCovProbe.Count[" in proc.stdout


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
    parser.add_argument(
        "--profile",
        help="reuse an existing coverage profile instead of running the suite. "
        "It MUST come from a -count=1 run against the current tree (see "
        "run_coverage_profile); a stale or cached profile reports phantom "
        "uncovered lines. The caller owns the file and this script never deletes it.",
    )
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
    renames = rename_pairs(root, diff_args)
    files = [
        f for f in changed_go_files(root, diff_args)
        if not f.endswith("_test.go")
        and not any(fnmatch.fnmatch(f, g) for g in excludes)
    ]
    if renames:
        print(
            f"diff_coverage: {len(renames)} renamed file(s) detected; "
            f"comparing each against its source instead of the merged base",
            file=sys.stderr,
        )
    if not files:
        print("diff_coverage: no changed non-test Go files in scope; skipping")
        return 0

    per_file_lines = {}
    per_file_text: dict[str, str | None] = {}
    for f in files:
        rename_source = renames.get(f)
        text = tip_file_text(root, diff_args, f)
        lines = executable_lines(text, changed_lines(root, diff_args, f, rename_source))
        if lines:
            per_file_lines[f] = lines
            per_file_text[f] = text
    if not per_file_lines:
        print("diff_coverage: no changed non-test Go lines in scope; skipping")
        return 0

    # A caller-supplied profile is reused as-is. `make verify` runs the
    # instrumented suite once and shares that profile with this gate, instead
    # of paying for a second full run of the same tests.
    supplied = Path(args.profile) if args.profile else None
    if supplied is not None and not supplied.is_file():
        print(f"diff_coverage: --profile {supplied} does not exist", file=sys.stderr)
        return 2
    profile = supplied if supplied is not None else run_coverage_profile(root)
    try:
        blocks_by_file = parse_profile(profile, module_path(root))
    finally:
        if supplied is None:
            profile.unlink(missing_ok=True)

    total_checked = 0
    total_uncovered = 0
    findings: list[str] = []
    covered_packages = {str(Path(f).parent) for f in blocks_by_file}
    no_statements: list[str] = []
    for f, lines in sorted(per_file_lines.items()):
        if f not in blocks_by_file:
            if str(Path(f).parent) in covered_packages or not file_has_statements(root, f, per_file_text[f]):
                # The file contributed no blocks for one of two reasons. Either
                # the package IS linked into a tested binary and the profile is
                # real, so this file holds no statements in THIS build -
                # declarations only, or a build constraint excluded it on this
                # platform. Or the whole package is declarations, so it emits no
                # counters and can never appear in any profile. Neither case can
                # be executed by any test, so the file is recorded as unchecked
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
        known = load_known_uncovered(root)
        accepted: list[str] = []
        remaining: list[str] = []
        for finding in findings:
            path, _, lineno = finding.rpartition(":")
            if path in known and int(lineno) in known[path][0]:
                accepted.append(f"{finding} ({known[path][1]})")
            else:
                remaining.append(finding)
        if accepted:
            print(f"diff_coverage: {len(accepted)} line(s) accepted as known-uncovered "
                  f"(.mivia/policy/diff-coverage.json):", file=sys.stderr)
            for line in accepted:
                print(f"  {line}", file=sys.stderr)
        if remaining:
            print(f"diff_coverage: {len(remaining)}/{total_checked} changed statement line(s) not covered by any test:")
            for finding in remaining:
                print(f"  {finding}")
            return 1

    print(f"diff_coverage: {total_checked}/{total_checked} changed statement line(s) covered")
    return 0


if __name__ == "__main__":
    sys.exit(main())
