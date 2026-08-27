#!/usr/bin/env python3
"""Contract tests for scripts/check_prose.py."""

from __future__ import annotations

import importlib.util
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "check_prose.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("check_prose", CHECKER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_prose.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_short_sentence_passes() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "short.md"
        f.write_text("This is a short sentence. It stays under the cap.\n", encoding="utf-8")
        assert mod.check_file(f, f.relative_to(td)) == []


def test_long_sentence_flagged() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "long.md"
        words = " ".join(f"word{i}" for i in range(30))
        f.write_text(f"{words}.\n", encoding="utf-8")
        violations = mod.check_file(f, f.relative_to(td))
        assert len(violations) == 1, violations
        assert "30 words" in violations[0], violations


def test_code_fence_exempt() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "fenced.md"
        words = " ".join(f"word{i}" for i in range(30))
        f.write_text(f"```\n{words}.\n```\n", encoding="utf-8")
        assert mod.check_file(f, f.relative_to(td)) == []


def test_heading_and_list_exempt() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "structure.md"
        words = " ".join(f"word{i}" for i in range(30))
        f.write_text(f"# {words}\n- {words}\n| {words} |\n", encoding="utf-8")
        assert mod.check_file(f, f.relative_to(td)) == []


def test_conflict_marker_flagged() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "conflict.md"
        f.write_text(
            "Intro sentence.\n<<<<<<< HEAD\nours.\n=======\ntheirs.\n>>>>>>> branch\n",
            encoding="utf-8",
        )
        violations = mod.check_file(f, f.relative_to(td))
        markers = [v for v in violations if "merge-conflict marker" in v]
        assert len(markers) == 3, violations


def test_at_cap_passes_over_cap_fails() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        f = Path(td) / "boundary.md"
        exact = " ".join(f"w{i}" for i in range(25))
        over = " ".join(f"w{i}" for i in range(26))
        f.write_text(f"{exact}.\n{over}.\n", encoding="utf-8")
        violations = mod.check_file(f, f.relative_to(td))
        assert len(violations) == 1, violations
        assert "26 words" in violations[0], violations


def main() -> None:
    test_short_sentence_passes()
    test_long_sentence_flagged()
    test_code_fence_exempt()
    test_heading_and_list_exempt()
    test_conflict_marker_flagged()
    test_at_cap_passes_over_cap_fails()
    print("test_check_prose: ok")


if __name__ == "__main__":
    main()
