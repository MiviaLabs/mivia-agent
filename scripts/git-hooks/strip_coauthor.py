#!/usr/bin/env python3
"""Strip disallowed Co-authored-by trailer lines from commit messages.

A line is removed only when the email inside angle brackets matches an
address in STRIP_EMAILS. All other lines - including the protected Mivia
Agent co-author line (<noreply@mivia.app>) and unrelated trailers - are
kept byte-for-byte. The hook and the history rewrite script share this
module, so the email list stays in sync everywhere.

Usage:
    strip_coauthor.py <commit-msg-file>   # edit the file in place (hook mode)
    strip_coauthor.py -                   # read stdin, write stdout (rewrite mode)
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

# Emails whose "Co-authored-by: Name <email>" trailer lines are removed.
# Mivia Agent (<noreply@mivia.app>) is intentionally never listed here.
STRIP_EMAILS = {
    "noreply@anthropic.com",  # Claude Opus 5, Claude Sonnet 5, ...
    "hook-test@example.invalid",  # test-identity junk
}

_CO_AUTHOR_RE = re.compile(
    r"^[ \t]*[Cc][Oo]-[Aa]uthored-[Bb]y:[ \t]*"
    r"(?P<name>.*?)[ \t]*<(?P<email>[^<>]+)>[ \t]*\r?$"
)


def strip_coauthor_lines(text: str) -> str:
    """Return text without matching Co-authored-by lines (case-insensitive email)."""
    emails = {email.lower() for email in STRIP_EMAILS}
    kept = [
        line
        for line in text.splitlines(keepends=True)
        if not _matches(line, emails)
    ]
    return "".join(kept)


def _matches(line: str, emails: set[str]) -> bool:
    match = _CO_AUTHOR_RE.match(line)
    if match is None:
        return False
    return match.group("email").strip().lower() in emails


def main(argv: list[str] | None = None) -> int:
    args = list(sys.argv[1:] if argv is None else argv)
    if not args:
        print("usage: strip_coauthor.py <commit-msg-file | ->", file=sys.stderr)
        return 2
    target = args[0]
    if target == "-":
        sys.stdout.write(strip_coauthor_lines(sys.stdin.read()))
        return 0
    path = Path(target)
    try:
        original = path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        print(f"strip_coauthor: cannot read {path}: {exc}", file=sys.stderr)
        return 1
    stripped = strip_coauthor_lines(original)
    if stripped == original:
        return 0
    try:
        path.write_text(stripped, encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        print(f"strip_coauthor: cannot write {path}: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
