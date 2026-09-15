#!/usr/bin/env python3
"""Gate: doc prose follows the writing standard
(.agents/rules/90-writing-standard-ste100.md). Checks sentence length in
docs/**/*.md: one idea per sentence, at most 25 words. This mirrors the
lenient end of that rule's 20/25 instructional/descriptive split - a
mechanical backstop, not a replacement for the fuller style rule. Code
fences, headings, and list lines are exempt.

Also rejects a leftover Git merge-conflict marker in any tracked
docs/**/*.md file, so an unresolved merge never passes make verify
silently.
"""
import re
import sys
from pathlib import Path

MAX_WORDS = 25
SENTENCE = re.compile(r"[^.!?]+[.!?]")
CONFLICT_MARKER = re.compile(r"^(<{7} |={7}$|>{7} )")


def check_file(path: Path, rel: Path) -> list[str]:
    violations = []
    in_fence = False
    for n, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        stripped = line.strip()
        if CONFLICT_MARKER.match(line):
            violations.append(f"{rel}:{n}: leftover merge-conflict marker")
            continue
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence or stripped.startswith(("#", "-", "|")):
            continue
        for m in SENTENCE.finditer(stripped):
            words = len(m.group().split())
            if words > MAX_WORDS:
                violations.append(f"{rel}:{n}: sentence has {words} words > {MAX_WORDS}")
    return violations


def main(argv: list[str]) -> int:
    root = Path(__file__).resolve().parent.parent
    violations = []
    if argv:
        # Explicit paths: check only what was passed. Directories are
        # expanded to their *.md files (recursively).
        paths = []
        for arg in argv:
            path = Path(arg)
            if not path.is_absolute():
                path = root / path
            if path.is_dir():
                md = sorted(path.rglob("*.md"))
                if not md:
                    print(f"{arg}: no .md files under directory", file=sys.stderr)
                    return 2
                paths.extend(md)
                continue
            if path.suffix != ".md":
                print(f"{arg}: not a .md file", file=sys.stderr)
                return 2
            if not path.is_file():
                print(f"{arg}: no such file", file=sys.stderr)
                return 2
            paths.append(path)
    else:
        paths = sorted((root / "docs").rglob("*.md"))
    for path in paths:
        violations.extend(check_file(path, path.relative_to(root)))
    if violations:
        print("\n".join(violations))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
