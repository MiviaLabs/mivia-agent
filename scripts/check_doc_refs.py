#!/usr/bin/env python3
"""Gate doc references in Go comments, package docs, and plan labels.

Three classes of violations exist in this repo:

(a) A Go comment in cmd/ or internal/ cites a path like docs/...md or
    .agents/...md that does not exist on disk. The cite then misleads the
    next reader.
(b) A package under internal/ carries no package doc. Its godoc page then
    starts with no context.
(c) A Go comment in cmd/ or internal/ carries a plan-work label (a slice
    tag like "plan D3" or a section mark like "§4"). These labels leak
    planning scratch into shipped code.

The default run enforces only class (c), through a committed baseline. The
baseline file lists every live "path:line" match, one per line, sorted. The
baseline may only shrink: a match must be in the baseline to pass, and a
baseline entry whose line no longer carries a match must be removed in the
same change. Run with --strict to also enforce classes (a) and (b); later
slices fix the tree and flip that on in the Makefile.

Run --update-baseline to regenerate the baseline from the current tree.
"""

from __future__ import annotations

import argparse
import functools
import re
import subprocess
import sys
from pathlib import Path

from verify_common import ROOT
from verify_common import fail as _fail

fail = functools.partial(_fail, prefix="check_doc_refs")

BASELINE = Path("scripts/doc_refs_baseline.txt")
SCAN_DIRS = ("cmd", "internal")
DOC_REF = re.compile(r"(?:docs|\.agents)/[A-Za-z0-9_./-]+\.(?:md|markdown)\b")
# Case-sensitive, per the approved plan. Search applies to whole comment lines.
PLAN_LABEL = re.compile(
    r"§[0-9]|plan D[0-9]|Stage 0|Phase [0-9]|B\.[0-9] #|round [0-9]|plan tools/|locked plan"
)

# Comment extraction: strip string literals, then collect // line comments
# and /* */ block comments. A // inside a string must not count, so strings
# go first. Raw strings and escapes: the lexer below handles both.
LINE_COMMENT = re.compile(r"//.*")
BLOCK_SPAN = re.compile(r"/\*.*?\*/", re.DOTALL)
STRING_SPAN = re.compile(r'"(?:\\.|[^"\\])*"|`[^`]*`|\'(?:\\.|[^\'\\])*\'')


def strip_strings(text: str) -> str:
    return STRING_SPAN.sub(lambda m: '""' if not m.group(0).startswith("`") else "``", text)


def comment_lines(rel: str, text: str) -> list[tuple[int, str]]:
    """Yield (lineno, comment text) for every Go comment in the file.

    The returned text holds comment content only. A doc-ref or label scan
    then runs on whole comment lines, as the plan requires.
    """
    text = strip_strings(text)
    out: list[tuple[int, str]] = []
    for m in BLOCK_SPAN.finditer(text):
        start_line = text.count("\n", 0, m.start()) + 1
        for i, line in enumerate(m.group(0).split("\n")):
            out.append((start_line + i, line))
    for m in LINE_COMMENT.finditer(text):
        line_no = text.count("\n", 0, m.start()) + 1
        # Skip // that opens a block-comment marker we already took (none).
        # Skip // inside /* */ spans: the block pass owns those.
        if any(s <= m.start() <= e for s, e in _block_spans(text)):
            continue
        out.append((line_no, m.group(0)[2:]))
    out.sort()
    return [(n, t) for n, t in out if n and t]


def _block_spans(text: str) -> list[tuple[int, int]]:
    return [(m.start(), m.end()) for m in BLOCK_SPAN.finditer(text)]


def go_files(root: Path) -> list[Path]:
    out: list[Path] = []
    for d in SCAN_DIRS:
        base = root / d
        if base.is_dir():
            out.extend(sorted(base.rglob("*.go")))
    return out


def find_dangling_doc_refs(root: Path) -> list[str]:
    """Class (a): a cited docs/ or .agents/ markdown path that does not exist."""
    violations: list[str] = []
    for path in go_files(root):
        rel = path.relative_to(root).as_posix()
        text = path.read_text(encoding="utf-8", errors="replace")
        for line_no, line in comment_lines(rel, text):
            for m in DOC_REF.finditer(line):
                target = root / m.group(0)
                if not target.is_file():
                    violations.append(
                        f"{rel}:{line_no}: comment cites {m.group(0)}, "
                        "which does not exist on disk"
                    )
    return violations


def check_pkg_docs(pkg_docs: dict[str, str]) -> list[str]:
    """Class (b), pure: map of import path -> package doc. Empty doc fails.

    Takes a map so tests run without `go list`. The real run feeds this
    from `go list -json ./internal/...`, which skips grouping dirs that
    hold no Go files.
    """
    violations: list[str] = []
    for pkg in sorted(pkg_docs):
        if pkg_docs[pkg].strip() == "":
            violations.append(f"{pkg}: package carries no package doc comment")
    return violations


def pkg_docs_via_go_list(root: Path) -> dict[str, str]:
    """Feed check_pkg_docs from the live tree.

    -find skips dependency resolution. Plain -json walks the whole dep
    graph and takes minutes on this repo; -find keeps the run fast.
    """
    proc = subprocess.run(
        ["go", "list", "-find", "-f", "{{.ImportPath}}\t{{.Doc}}", "./internal/..."],
        cwd=root,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        fail(f"go list ./internal/... failed: {proc.stderr.strip()}")
    docs: dict[str, str] = {}
    for raw in proc.stdout.split("\n"):
        if not raw:
            continue
        pkg, _, doc = raw.partition("\t")
        docs[pkg] = doc
    return docs


def find_plan_label_matches(root: Path) -> list[str]:
    """Class (c): every "path:line" whose comment carries a plan label."""
    matches: list[str] = []
    for path in go_files(root):
        rel = path.relative_to(root).as_posix()
        text = path.read_text(encoding="utf-8", errors="replace")
        for line_no, line in comment_lines(rel, text):
            if PLAN_LABEL.search(line):
                matches.append(f"{rel}:{line_no}")
    return sorted(matches)


def load_baseline(root: Path, baseline: Path | None = None) -> set[str]:
    base = root / BASELINE if baseline is None else baseline
    if not base.is_file():
        fail(f"{BASELINE} does not exist. Run --update-baseline to create it.")
    entries: set[str] = set()
    for raw in base.read_text(encoding="utf-8").split("\n"):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        entries.add(line)
    return entries


def check_plan_labels(root: Path, baseline: Path | None = None) -> list[str]:
    """Class (c) against the baseline. Shrink-only: a live match must be in
    the baseline, and a baseline entry must still carry a live match."""
    live = find_plan_label_matches(root)
    if baseline is None:
        baseline = root / BASELINE
    entries = load_baseline(root, baseline)
    violations = [f"{m}: plan-label match is not in {BASELINE}" for m in live if m not in entries]
    for e in sorted(entries):
        if e not in live:
            violations.append(
                f"{e}: baseline entry has no live plan-label match. "
                "Remove the entry from the baseline in the same change."
            )
    return violations


def update_baseline(root: Path) -> None:
    live = find_plan_label_matches(root)
    base = root / BASELINE
    body = "\n".join(live) + "\n" if live else ""
    base.write_text(
        "# Plan-label baseline. Each line is a Go comment line in cmd/ or\n"
        "# internal/ that carries a plan-work label. The baseline may only\n"
        "# shrink: fix the comment, then remove its line here in the same\n"
        "# change. The gate fails on any entry with no live match.\n" + body,
        encoding="utf-8",
    )
    print(f"check_doc_refs: wrote {len(live)} baseline entries to {BASELINE}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--strict", action="store_true", help="also enforce dangling doc refs and package docs")
    parser.add_argument("--update-baseline", action="store_true", help="regenerate the baseline from the current tree")
    parser.add_argument("--root", default=str(ROOT), help="repo root to scan")
    args = parser.parse_args(argv)
    root = Path(args.root)

    if args.update_baseline:
        update_baseline(root)
        return 0

    violations = check_plan_labels(root)
    if args.strict:
        violations += find_dangling_doc_refs(root)
        violations += check_pkg_docs(pkg_docs_via_go_list(root))
    for v in violations:
        print(v, file=sys.stderr)
    if violations:
        fail(f"{len(violations)} violation(s). Fix the comments or update the baseline; the baseline may only shrink.")
    print("check_doc_refs: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
