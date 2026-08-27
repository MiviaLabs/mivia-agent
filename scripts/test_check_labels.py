#!/usr/bin/env python3
"""Contract tests for scripts/check_labels.py.

Test fixtures build label tokens (like "C" + "3") from parts instead of
writing them as a literal source substring: check_labels.py scans this
file too, and a literal fixture label would trip the very gate this file
tests, in this file's own source.
"""

from __future__ import annotations

import importlib.util
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "check_labels.py"


def lbl(letter: str, digit: str) -> str:
    """Build a label token from parts so this file's own source never
    contains the literal substring (see module docstring)."""
    return letter + digit


def load_mod():
    spec = importlib.util.spec_from_file_location("check_labels", CHECKER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_labels.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_label_pattern_matches_letter_a_to_g_plus_digit() -> None:
    mod = load_mod()
    assert mod.LABEL.search(f"finding {lbl('C', '3')} confirmed".encode())
    assert mod.LABEL.search(f"see {lbl('D', '1')} and {lbl('D', '2')}".encode())
    assert mod.LABEL.search(f"{lbl('G', '9')} is the last one".encode())


def test_label_pattern_rejects_out_of_range_letters() -> None:
    mod = load_mod()
    assert mod.LABEL.search(f"{lbl('H', '3')} is not a severity letter".encode()) is None
    assert mod.LABEL.search(f"{lbl('Z', '9')} is not a severity letter".encode()) is None


def test_label_pattern_is_whole_word() -> None:
    mod = load_mod()
    # A leading "A" glued to the token breaks the word boundary before it.
    assert mod.LABEL.search(f"A{lbl('C', '3')}PO is a droid name".encode()) is None


def test_walk_skips_git_and_semgrep() -> None:
    mod = load_mod()
    token = lbl("C", "3") + "\n"
    with tempfile.TemporaryDirectory() as td:
        d = Path(td)
        (d / ".git").mkdir()
        (d / ".git" / "hidden.txt").write_text(token, encoding="utf-8")
        (d / "semgrep").mkdir()
        (d / "semgrep" / "rule.yml").write_text(token, encoding="utf-8")
        (d / "keep.md").write_text("no label here\n", encoding="utf-8")
        found = {p.name for p in mod.walk(d)}
        assert found == {"keep.md"}


def test_walk_skips_generated_worktrees_by_full_relative_path() -> None:
    mod = load_mod()
    token = lbl("C", "3") + "\n"
    with tempfile.TemporaryDirectory() as td:
        d = Path(td)
        deep = d / ".mivia" / "worktrees" / "run-1" / "docs"
        deep.mkdir(parents=True)
        (deep / "leaked.md").write_text(token, encoding="utf-8")
        (d / "docs").mkdir()
        (d / "docs" / "keep.md").write_text("no label\n", encoding="utf-8")
        found = {str(p.relative_to(d)) for p in mod.walk(d)}
        assert found == {"docs/keep.md"}


def test_walk_skips_binary_extensions_and_basenames() -> None:
    mod = load_mod()
    token = lbl("C", "3").encode() + b"\x00\x01"
    with tempfile.TemporaryDirectory() as td:
        d = Path(td)
        (d / "memory.db").write_bytes(token)
        (d / "mivia").write_bytes(token)
        (d / "notes.md").write_text("no label\n", encoding="utf-8")
        found = {p.name for p in mod.walk(d)}
        assert found == {"notes.md"}


def test_binary_file_does_not_crash_the_scan() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        d = Path(td)
        f = d / "blob.bin"
        f.write_bytes(bytes(range(256)) + b" " + lbl("C", "3").encode() + b" ")
        # Must not raise (byte scan, never decodes text).
        data = f.read_bytes()
        assert mod.LABEL.search(data)


def main() -> None:
    test_label_pattern_matches_letter_a_to_g_plus_digit()
    test_label_pattern_rejects_out_of_range_letters()
    test_label_pattern_is_whole_word()
    test_walk_skips_git_and_semgrep()
    test_walk_skips_generated_worktrees_by_full_relative_path()
    test_walk_skips_binary_extensions_and_basenames()
    test_binary_file_does_not_crash_the_scan()
    print("test_check_labels: ok")


if __name__ == "__main__":
    main()
