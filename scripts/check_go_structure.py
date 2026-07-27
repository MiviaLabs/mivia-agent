#!/usr/bin/env python3
"""Enforce Go file LOC and function length limits (anti-spaghetti for agents).

Modes:
  --staged     only staged *.go (pre-commit)
  --all        all tracked *.go under cmd/ and internal/ (pre-push / make)
  --paths ...  explicit paths

Exit codes:
  0 = OK (warnings only)
  1 = hard violations
  2 = usage / config error

Policy: .ai/policy/go-structure.json
"""
from __future__ import annotations

import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
POLICY_PATH = ROOT / ".ai" / "policy" / "go-structure.json"

FUNC_START = re.compile(
    r"^func\s+"
    r"(?:\([^)]*\)\s*)?"  # optional receiver
    r"([A-Za-z_][A-Za-z0-9_]*)"  # name
)


def load_policy() -> dict:
    if not POLICY_PATH.is_file():
        print(f"check_go_structure: missing policy {POLICY_PATH}", file=sys.stderr)
        sys.exit(2)
    return json.loads(POLICY_PATH.read_text(encoding="utf-8"))


def git_staged_go() -> list[Path]:
    r = subprocess.run(
        ["git", "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", "*.go"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if r.returncode != 0:
        print(f"check_go_structure: git diff failed: {r.stderr}", file=sys.stderr)
        sys.exit(2)
    out = []
    for f in r.stdout.strip("\0").split("\0"):
        if f:
            out.append(ROOT / f)
    return out


def git_tracked_go() -> list[Path]:
    r = subprocess.run(
        ["git", "ls-files", "-z", "--", "cmd/**/*.go", "internal/**/*.go"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if r.returncode != 0:
        # fallback glob
        return sorted(ROOT.joinpath("internal").rglob("*.go")) + sorted(
            ROOT.joinpath("cmd").rglob("*.go")
        )
    paths = []
    for f in r.stdout.strip("\0").split("\0"):
        if f.endswith(".go"):
            paths.append(ROOT / f)
    return paths


def is_test(path: Path) -> bool:
    return path.name.endswith("_test.go")


def rel(path: Path) -> str:
    try:
        return path.resolve().relative_to(ROOT.resolve()).as_posix()
    except ValueError:
        return path.as_posix()


def count_file_lines(path: Path) -> int:
    text = path.read_text(encoding="utf-8", errors="replace")
    if not text:
        return 0
    # Match wc -l style: count newlines; if no trailing newline, still count last line.
    n = text.count("\n")
    if not text.endswith("\n") and text.strip():
        n += 1
    return n


def parse_functions(path: Path) -> list[tuple[str, int, int]]:
    """Return list of (name, start_line, end_line) 1-indexed inclusive."""
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    funcs: list[tuple[str, int, int]] = []
    i = 0
    n = len(lines)
    while i < n:
        m = FUNC_START.match(lines[i])
        if not m:
            i += 1
            continue
        name = m.group(1)
        start = i + 1
        # Find opening brace on this or following lines (signature may wrap).
        brace_line = i
        found = False
        while brace_line < n and brace_line < i + 15:
            if "{" in lines[brace_line]:
                found = True
                break
            # interface/abstract-less: single-line func without body shouldn't happen often
            if brace_line > i and lines[brace_line].strip().startswith("func "):
                break
            brace_line += 1
        if not found:
            i += 1
            continue
        depth = 0
        j = brace_line
        while j < n:
            for ch in lines[j]:
                if ch == "{":
                    depth += 1
                elif ch == "}":
                    depth -= 1
                    if depth == 0:
                        end = j + 1
                        funcs.append((name, start, end))
                        i = j + 1
                        break
            else:
                j += 1
                continue
            break
        else:
            i += 1
    return funcs


def check_paths(paths: list[Path], policy: dict, *, strict: bool) -> int:
    fl = policy["fileLines"]
    fn = policy["funcLines"]
    baseline = (policy.get("baseline") or {}).get("files") or {}
    excludes = policy.get("excludeGlobs") or []
    hard_fail = 0
    warnings = 0

    for path in paths:
        if not path.is_file():
            continue
        r = rel(path)
        if (
            r.startswith("vendor/")
            or "/vendor/" in r
            or any(fnmatch.fnmatch(r, pattern) for pattern in excludes)
        ):
            continue
        lines = count_file_lines(path)
        test = is_test(path)
        soft = int(fl["testSoft"] if test else fl["soft"])
        hard = int(fl["testHard"] if test else fl["hard"])
        base = baseline.get(r)
        base_max = int(base["maxLines"]) if base else None

        # File LOC
        if base_max is not None:
            if lines > base_max:
                print(
                    f"HARD file LOC growth: {r} has {lines} lines "
                    f"(baseline max {base_max}). Split before growing.",
                    file=sys.stderr,
                )
                hard_fail += 1
            elif lines > soft:
                print(
                    f"WARN grandfathered file still oversized: {r} has {lines} lines "
                    f"(soft {soft}, baseline max {base_max}). Prefer splitting.",
                    file=sys.stderr,
                )
                warnings += 1
        else:
            if lines > hard:
                print(
                    f"HARD file LOC: {r} has {lines} lines (hard max {hard}). "
                    f"Split the file before committing.",
                    file=sys.stderr,
                )
                hard_fail += 1
            elif lines > soft:
                print(
                    f"WARN file LOC: {r} has {lines} lines (soft max {soft}). "
                    f"Consider splitting soon.",
                    file=sys.stderr,
                )
                warnings += 1

        # Functions — skip grandfathered whole-file debt (until split), still warn soft.
        funcs = parse_functions(path)
        for name, start, end in funcs:
            flines = end - start + 1
            fsoft = int(fn["soft"])
            fhard = int(fn["hard"])
            if flines > fhard:
                if base_max is not None:
                    print(
                        f"WARN long function (grandfathered file): {r}:{name} "
                        f"L{start}-L{end} ({flines} lines, hard {fhard})",
                        file=sys.stderr,
                    )
                    warnings += 1
                else:
                    print(
                        f"HARD function LOC: {r}:{name} L{start}-L{end} "
                        f"({flines} lines, hard max {fhard}). Extract helpers.",
                        file=sys.stderr,
                    )
                    hard_fail += 1
            elif flines > fsoft:
                print(
                    f"WARN function LOC: {r}:{name} L{start}-L{end} "
                    f"({flines} lines, soft max {fsoft})",
                    file=sys.stderr,
                )
                warnings += 1

    if strict and warnings:
        hard_fail += warnings
        print(f"check_go_structure: strict mode promotes {warnings} warning(s) to hard failures", file=sys.stderr)
    if warnings and not hard_fail:
        print(
            f"check_go_structure: {warnings} warning(s), 0 hard failures",
            file=sys.stderr,
        )
    if hard_fail:
        print(
            f"\ncheck_go_structure: {hard_fail} hard violation(s), {warnings} warning(s).\n"
            f"Policy: {POLICY_PATH.relative_to(ROOT)}\n"
            f"Split files/functions or (only when reducing debt) lower baseline maxLines.\n",
            file=sys.stderr,
        )
        return 1
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    g = ap.add_mutually_exclusive_group()
    g.add_argument("--staged", action="store_true", help="Staged Go files only")
    g.add_argument("--all", action="store_true", help="All tracked Go under cmd/internal")
    ap.add_argument("paths", nargs="*", help="Explicit paths")
    ap.add_argument(
        "--strict",
        action="store_true",
        help="Promote all warnings to failures",
    )
    args = ap.parse_args()
    policy = load_policy()

    if args.paths:
        paths = [Path(p) if Path(p).is_absolute() else ROOT / p for p in args.paths]
    elif args.all:
        paths = git_tracked_go()
    else:
        # default staged for hook ergonomics
        paths = git_staged_go()

    # Only production + tests under cmd/internal if explicit path outside, still check
    paths = [p for p in paths if p.suffix == ".go"]
    if not paths:
        return 0
    return check_paths(paths, policy, strict=args.strict)


if __name__ == "__main__":
    sys.exit(main())
