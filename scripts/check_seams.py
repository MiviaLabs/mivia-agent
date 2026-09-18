#!/usr/bin/env python3
"""Seam-count gate for internal/cli packages.

Counts package-level nil seam variables (func-typed and error-typed) declared
in non-test Go files under internal/cli/**, holds each package's count to a
committed baseline in .mivia/policy/seam-baseline.json, and fails when a count
rises. A count below baseline passes but is reported so the baseline can be
lowered in the same change that removed the seams.

The gate also holds the internal/cli/chat fan-out over internal/workflows/*
(the number of chat files importing the workflow domain packages) to its
baseline; the composition-root cleanup must push this number down, never up.

Usage:
    python3 scripts/check_seams.py                # check against baseline
    python3 scripts/check_seams.py --generate     # (re)write the baseline

Contract tests: scripts/test_check_seams.py
"""

import argparse
import json
import os
import re
import sys

MODULE_PREFIX = "github.com/MiviaLabs/mivia-agent/"
CLI_DIR = os.path.join("internal", "cli")
CHAT_DIR = os.path.join("internal", "cli", "chat")
WORKFLOWS_IMPORT = "mivia-agent/internal/workflows/"
BASELINE_REL = os.path.join(".mivia", "policy", "seam-baseline.json")

VAR_BLOCK_OPEN = re.compile(r"^var\s*\(")
VAR_BLOCK_CLOSE = re.compile(r"^\)")
_NAMES = r"([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)"
BLOCK_FUNC_SEAM = re.compile(r"^\t%s\s+func\(" % _NAMES)
BLOCK_ERROR_SEAM = re.compile(r"^\t%s\s+error\s*$" % _NAMES)
SOLO_FUNC_SEAM = re.compile(r"^var\s+%s\s+func\(" % _NAMES)
SOLO_ERROR_SEAM = re.compile(r"^var\s+%s\s+error\s*$" % _NAMES)


def blank_non_code(text, blank_strings=True):
    """Blank comments and string/rune literal contents with spaces, keeping
    every byte position and newline so line numbers are stable. What remains
    is safe to regex as Go structure and identifiers."""
    out = []
    i = 0
    n = len(text)
    state = None
    while i < n:
        ch = text[i]
        if state is None:
            if ch == "/" and i + 1 < n and text[i + 1] == "/":
                state = "line"
                out.append("  ")
                i += 2
                continue
            if ch == "/" and i + 1 < n and text[i + 1] == "*":
                state = "block"
                out.append("  ")
                i += 2
                continue
            if ch == '"':
                state = "str"
                out.append(ch)
                i += 1
                if not blank_strings:
                    state = "str_keep"
                continue
            if ch == "`":
                # Raw strings always blank: import paths are never raw
                # strings, and their multi-line content must not leak into
                # line-based import scanners.
                state = "raw"
                out.append(ch)
                i += 1
                continue
            if ch == "'":
                state = "rune"
                out.append(ch)
                i += 1
                if not blank_strings:
                    state = "rune_keep"
                continue
            out.append(ch)
            i += 1
            continue
        if state == "line":
            if ch == "\n":
                state = None
                out.append(ch)
            else:
                out.append(" ")
            i += 1
            continue
        if state == "block":
            if ch == "*" and i + 1 < n and text[i + 1] == "/":
                out.append("  ")
                i += 2
                state = None
                continue
            out.append(ch if ch == "\n" else " ")
            i += 1
            continue
        if ch == "\\" and state in ("str", "rune"):
            out.append("  " if not state.endswith("_keep") else ch + (text[i + 1] if i + 1 < n else " "))
            i += 2
            continue
        if (state in ("str", "str_keep") and ch == '"') or (state == "rune" and ch == "'") or (state in ("rune_keep", "raw") and ch == "`"):
            state = None
            out.append(ch)
            i += 1
            continue
        out.append(ch if (ch == "\n" or state.endswith("_keep")) else " ")
        i += 1
    return "".join(out)


def go_files(root, subdir):
    base = os.path.join(root, subdir)
    for dirpath, _dirnames, filenames in os.walk(base):
        for name in sorted(filenames):
            if name.endswith(".go") and not name.endswith("_test.go"):
                yield os.path.join(dirpath, name)


def pkg_path(root, path):
    rel = os.path.relpath(path, root)
    return os.path.dirname(rel)


def collect_seams(root):
    """Return {pkg_path: [(name, file, line_no)]} for seam declarations."""
    seams = {}
    for path in go_files(root, CLI_DIR):
        pkg = pkg_path(root, path)
        in_block = False
        with open(path, encoding="utf-8") as fh:
            code = blank_non_code(fh.read())
            for lineno, line in enumerate(code.splitlines(), 1):
                stripped = line
                if VAR_BLOCK_OPEN.match(stripped):
                    in_block = True
                    continue
                if in_block and VAR_BLOCK_CLOSE.match(stripped):
                    in_block = False
                    continue
                if in_block:
                    m = BLOCK_FUNC_SEAM.match(stripped) or BLOCK_ERROR_SEAM.match(stripped)
                else:
                    m = SOLO_FUNC_SEAM.match(stripped) or SOLO_ERROR_SEAM.match(stripped)
                if m:
                    for name in re.split(r"\s*,\s*", m.group(1)):
                        seams.setdefault(pkg, []).append((name, path, lineno))
    return seams


def go_source_files(root):
    """Yield (abs_path, pkg_path) for all non-test Go files under internal/."""
    for dirpath, dirnames, filenames in os.walk(os.path.join(root, "internal")):
        dirnames[:] = [d for d in dirnames if d != "testdata"]
        for name in sorted(filenames):
            if name.endswith(".go"):
                path = os.path.join(dirpath, name)
                yield path, pkg_path(root, path)


def collect_assignment_index(root):
    """Return [(abs_path, pkg_path, code_line)] for all Go files (tests
    included; test-only seams are a legitimate pattern here)."""
    entries = []
    for path, pkg in go_source_files(root):
        with open(path, encoding="utf-8") as fh:
            for line in blank_non_code(fh.read()).splitlines():
                entries.append((path, pkg, line))
    return entries


def collect_pkg_names(root):
    """Return {pkg_path: package_clause_name} from the first file per dir."""
    names = {}
    pkg_re = re.compile(r"^package\s+([A-Za-z_]\w*)")
    for path, pkg in go_source_files(root):
        if pkg in names:
            continue
        with open(path, encoding="utf-8") as fh:
            code = blank_non_code(fh.read())
        for line in code.splitlines():
            m = pkg_re.match(line.strip())
            if m:
                names[pkg] = m.group(1)
                break
    return names


def collect_file_imports(root, pkg_names):
    """Return {abs_path: {alias: module_path}} from each Go file's import
    block. Package-qualified seam assignments (workflow.F = ...) are counted
    only in files whose own import block binds that alias to the seam's
    package, so a same-named local in an unrelated file cannot clear a
    seam."""
    file_imports = {}
    import_re = re.compile(
        r"^(?:([A-Za-z_]\w*|\.)\s+)?\"(?:github\.com/MiviaLabs/mivia-agent/|)([^\"]+)\""
    )
    for path, _pkg in go_source_files(root):
        with open(path, encoding="utf-8") as fh:
            code = blank_non_code(fh.read(), blank_strings=False)
        aliases = {}
        in_import = False
        for line in code.splitlines():
            stripped = line.strip()
            spec_line = None
            if stripped.startswith("import ("):
                in_import = True
                # One-line form: import ( "path" )
                rest = stripped[len("import ("):].strip()
                if rest.endswith(")"):
                    spec_line = rest[:-1].strip()
                else:
                    continue
            elif in_import:
                if stripped == ")":
                    in_import = False
                    continue
                if not stripped:
                    continue
                spec_line = stripped
            elif stripped.startswith("import "):
                spec_line = stripped[len("import "):].strip()
            if spec_line is None:
                continue
            m = import_re.match(spec_line)
            if not (m and m.group(2)):
                continue
            modpath = m.group(2)
            alias = m.group(1)
            if alias not in ("_", "."):
                default = pkg_names.get(modpath, modpath.rsplit("/", 1)[-1])
                aliases[alias or default] = modpath
        file_imports[path] = aliases
    return file_imports



def is_assigned(name, pkg, entries, file_imports):
    list_tail = r"(?:\s*,\s*[A-Za-z_][\w.]*)*\s*=(?!=)"
    bare_pattern = re.compile(
        r"(?<![\w.])(?:[A-Za-z_]\w*\s*,\s*)*%s%s" % (re.escape(name), list_tail)
    )
    qual_cache = {}
    for _path, fpkg, line in entries:
        if fpkg == pkg and bare_pattern.search(line):
            return True
        aliases = file_imports.get(_path, {})
        qual_key = tuple(sorted(a for a, p in aliases.items() if p == pkg))
        if not qual_key:
            continue
        pattern = qual_cache.get(qual_key)
        if pattern is None:
            alts = []
            for alias in qual_key:
                alts.append(
                    r"(?<![\w.])%s\.(?:[A-Za-z_]\w*\s*,\s*)*%s%s"
                    % (re.escape(alias), re.escape(name), list_tail)
                )
            pattern = re.compile("|".join(alts))
            qual_cache[qual_key] = pattern
        if pattern.search(line):
            return True
    return False


IMPORT_SPEC = re.compile(
    r"^(?:([A-Za-z_]\w*|\.)\s+)?\"([^\"]+)\""
)


def chat_fanout(root):
    """Count chat files that import an internal/workflows/* package (real
    import specs only - comments and strings do not count)."""
    count = 0
    for path in go_files(root, CHAT_DIR):
        with open(path, encoding="utf-8") as fh:
            code = blank_non_code(fh.read(), blank_strings=False)
        in_import = False
        for line in code.splitlines():
            stripped = line.strip()
            spec_line = None
            if stripped.startswith("import ("):
                in_import = True
                rest = stripped[len("import ("):].strip()
                if rest.endswith(")"):
                    spec_line = rest[:-1].strip()
                else:
                    continue
            elif in_import:
                if stripped == ")":
                    in_import = False
                    continue
                if not stripped:
                    continue
                spec_line = stripped
            elif stripped.startswith("import "):
                spec_line = stripped[len("import "):].strip()
            if spec_line is None:
                continue
            m = IMPORT_SPEC.match(spec_line)
            if m and m.group(2).startswith("github.com/MiviaLabs/mivia-agent/internal/workflows/"):
                count += 1
                break
    return count


def load_baseline(root):
    path = os.path.join(root, BASELINE_REL)
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def write_baseline(root, seams, fanout):
    path = os.path.join(root, BASELINE_REL)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    doc = {
        "comment": (
            "Baseline for scripts/check_seams.py: per-package count of "
            "package-level nil seam vars (func- and error-typed) under "
            "internal/cli/**, plus internal/cli/chat's file fan-out over "
            "internal/workflows/*. Counts may only go down; regenerate with "
            "--generate when a removal lands."
        ),
        "seams": {pkg: len(entries) for pkg, entries in sorted(seams.items())},
        "chat_workflows_fanout": {CHAT_DIR.replace(os.sep, "/"): fanout},
        "ignore": [],
    }
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(doc, fh, indent=2)
        fh.write("\n")
    return doc


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    parser.add_argument("--generate", action="store_true", help="write the baseline and exit")
    args = parser.parse_args()

    seams = collect_seams(args.root)
    fanout = chat_fanout(args.root)

    if args.generate:
        doc = write_baseline(args.root, seams, fanout)
        for pkg, count in sorted(doc["seams"].items()):
            print("seams %s: %d" % (pkg, count))
        print("chat_workflows_fanout %s: %d" % (CHAT_DIR.replace(os.sep, "/"), fanout))
        return 0

    baseline = load_baseline(args.root)
    if baseline is None:
        print("seam gate: missing %s; run with --generate" % BASELINE_REL, file=sys.stderr)
        return 1

    failures = []
    ignored = set(baseline.get("ignore", []))
    assignment_lines = collect_assignment_index(args.root)
    pkg_names = collect_pkg_names(args.root)
    file_imports = collect_file_imports(args.root, pkg_names)

    for pkg in sorted(set(baseline.get("seams", {})) | set(seams)):
        count = len(seams.get(pkg, []))
        base = baseline.get("seams", {}).get(pkg, 0)
        if count > base:
            failures.append("seams %s: %d declared, baseline %d" % (pkg, count, base))
        elif count < base:
            print("seam gate: %s seams %d < baseline %d; lower the baseline with --generate" % (pkg, count, base))
        for name, path, lineno in seams.get(pkg, []):
            if name in ignored:
                continue
            if not is_assigned(name, pkg, assignment_lines, file_imports):
                failures.append("seam %s.%s declared at %s:%d is never assigned" % (pkg, name, path, lineno))

    chat_key = CHAT_DIR.replace(os.sep, "/")
    base_fanout = baseline.get("chat_workflows_fanout", {}).get(chat_key, 0)
    if fanout > base_fanout:
        failures.append("chat_workflows_fanout %s: %d files, baseline %d" % (chat_key, fanout, base_fanout))
    elif fanout < base_fanout:
        print("seam gate: %s fan-out %d < baseline %d; lower the baseline with --generate" % (chat_key, fanout, base_fanout))

    if failures:
        print("seam gate: FAIL")
        for failure in failures:
            print("  " + failure)
        return 1

    print("seam gate: OK (%d packages, chat fan-out %d)" % (len(seams), fanout))
    return 0


if __name__ == "__main__":
    sys.exit(main())
