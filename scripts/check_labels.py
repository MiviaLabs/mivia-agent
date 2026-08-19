#!/usr/bin/env python3
"""Gate: audit-finding labels never appear in comments, docs, or plans.

A label is a whole word: a letter A through G followed by a digit. Skills
such as bug-audit and panel-bug-audit assign labels like this to findings
during a review. The label must stay inside the review turn; it must never
get pasted verbatim into a shipped doc or code comment.

The scan reads file bytes and never decodes text, so a binary file cannot
crash it. It walks the physical tree and needs no Git repository. It skips
.git/ and semgrep/ (mirroring the Makefile marker-scan exclusions) plus this
repo's machine-generated, gitignored subtrees (.mivia/worktrees,
.mivia/runs, .mivia/sessions, .mivia/handoffs, .claude/worktrees) - those
hold ephemeral run copies, not shipped content, so a label surfacing there
is not a repo-hygiene violation. It also skips known binary file
extensions (sqlite databases, images, archives, build output): random
binary bytes match the label pattern by chance, and a byte-level hit
inside a database blob is not a leaked label in shipped prose or code.

A hit reports the file path and line. The script exits one. A clean tree
exits zero.
"""
import re
import sys
from pathlib import Path

LABEL = re.compile(rb"\b[A-G][0-9]\b")

# Root-relative paths to prune. Directory-only entries; matched against the
# path of the directory relative to ROOT.
SKIP_DIRS = {
    ".git",
    "semgrep",
    "vendor",
    "__pycache__",
    ".pytest_cache",
    ".codegraph",
    "dist",
    "bin",
    ".mivia/worktrees",
    ".mivia/runs",
    ".mivia/sessions",
    ".mivia/handoffs",
    ".claude/worktrees",
}

# Known-binary file extensions and exact basenames: never prose or code, so
# a byte-level label match inside one is noise, not a finding.
SKIP_EXTENSIONS = {
    ".db",
    ".db-wal",
    ".db-shm",
    ".sqlite",
    ".sqlite3",
    ".png",
    ".jpg",
    ".jpeg",
    ".gif",
    ".ico",
    ".pdf",
    ".zip",
    ".gz",
    ".tar",
    ".woff",
    ".woff2",
    ".exe",
    ".test",
    ".out",
}
SKIP_BASENAMES = {"mivia", "mivia-ui-demo"}


def walk(root: Path, base: Path | None = None):
    """Yield every file below root, sorted, skipping SKIP_DIRS/binaries.

    base is the top-level scan root, fixed across the recursion, so
    SKIP_DIRS entries like ".mivia/worktrees" match the full relative path
    at every depth (not just the immediate parent's rel path).
    """
    base = base or root
    for child in sorted(root.iterdir()):
        rel = child.relative_to(base).as_posix()
        if child.is_dir():
            # A SKIP_DIRS entry matches by its full root-relative path (for
            # multi-segment entries like ".mivia/worktrees") or by bare
            # basename (for a directory name banned at any depth, like
            # "__pycache__" under scripts/).
            if rel in SKIP_DIRS or child.name in SKIP_DIRS:
                continue
            yield from walk(child, base)
        else:
            if child.suffix in SKIP_EXTENSIONS or child.name in SKIP_BASENAMES:
                continue
            yield child


def main() -> int:
    root = Path(__file__).resolve().parent.parent
    problems = []
    for path in walk(root):
        try:
            data = path.read_bytes()
        except OSError as exc:
            problems.append(f"{path}: unreadable: {exc}")
            continue
        for n, line in enumerate(data.splitlines(), 1):
            if LABEL.search(line):
                problems.append(f"{path}:{n}: audit-finding label")
    if problems:
        print("\n".join(problems))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
