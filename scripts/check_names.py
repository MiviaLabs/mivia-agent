#!/usr/bin/env python3
"""Gate: reject Go files and function names containing process-artifact
keywords. File basenames and function declarations must not contain:
phase, tdd, perf, wip, draft, scratch, tmp, old, backup, or a version
suffix like _v2, _v3 (versioning belongs in git, not in file names).

The keyword check is camelCase-aware: a banned word must appear as its
own word inside the identifier (a lowercase run, a Capitalized word, or
an ALLCAPS acronym run), not as a bare substring. "Hold" must not trip
on "old"; "phase07Runner" must trip on "phase".

Exits non-zero on violations.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

BAD_WORDS = {"phase", "tdd", "perf", "wip", "draft", "scratch", "tmp", "old", "backup"}
VERSION_SUFFIX = re.compile(r"_v\d+", re.IGNORECASE)
FUNC_DECL = re.compile(r"\bfunc\s+(?:\([^)]*\)\s+)?(\w+)\s*\(")
# CAMEL_WORD tokenizes on letter-case and digit boundaries. Underscores and
# dots match none of the alternatives, so findall() skips them for free -
# the same tokenizer works unmodified on snake_case filenames (splitting
# "storage_release_holder_test" into storage/release/holder/test, so
# "holder" never collides with the banned word "old") and on Go identifiers
# (splitting "phase07Runner" into phase/07/Runner).
CAMEL_WORD = re.compile(r"[A-Z]+(?![a-z])|[A-Z][a-z]*|[a-z]+|[0-9]+")

SKIP_DIRS = {".git", "semgrep", "vendor", "worktrees"}


def _has_bad_word(name: str) -> bool:
    """True if name contains a banned keyword as its own word - a
    lowercase run, a Capitalized word, or an ALLCAPS acronym run - not
    merely as a substring (so "Hold" does not trip on "old")."""
    if VERSION_SUFFIX.search(name):
        return True
    words = (w.lower() for w in CAMEL_WORD.findall(name))
    return any(w in BAD_WORDS for w in words)


def check_file(path: Path, rel: Path) -> list[str]:
    violations = []
    if _has_bad_word(path.stem):
        violations.append(f"{rel}: filename contains a prohibited keyword")
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        violations.append(f"{rel}: unreadable: {exc}")
        return violations
    for n, line in enumerate(text.splitlines(), 1):
        for m in FUNC_DECL.finditer(line):
            if _has_bad_word(m.group(1)):
                violations.append(
                    f"{rel}:{n}: function name contains a prohibited keyword"
                )
                break
    return violations


def main() -> int:
    root = Path(__file__).resolve().parent.parent
    violations = []
    for path in sorted(root.rglob("*.go")):
        rel = path.relative_to(root)
        if any(part in SKIP_DIRS for part in rel.parts):
            continue
        violations.extend(check_file(path, rel))
    if violations:
        print("\n".join(violations))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
