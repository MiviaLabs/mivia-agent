#!/usr/bin/env python3
"""Contract tests for scripts/check_names.py."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "check_names.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("check_names", CHECKER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_names.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_camel_word_tokenizer_avoids_substring_false_positive() -> None:
    mod = load_mod()
    # "Hold" must not trip on the banned word "old": no whole word "old".
    assert mod._has_bad_word("Hold") is False
    assert mod._has_bad_word("holder") is False
    assert mod._has_bad_word("StorageReleaseHolder") is False


def test_camel_word_tokenizer_catches_real_hits() -> None:
    mod = load_mod()
    assert mod._has_bad_word("Old") is True
    assert mod._has_bad_word("oldValue") is True
    assert mod._has_bad_word("HandleTDDMode") is True
    assert mod._has_bad_word("phase07Runner") is True
    assert mod._has_bad_word("runPERFSuite") is True


def test_version_suffix_detected() -> None:
    mod = load_mod()
    assert mod._has_bad_word("parseConfig_v2") is True
    assert mod._has_bad_word("parseConfig_v10") is True
    assert mod._has_bad_word("parseConfigV2") is False  # no underscore, not the banned form


def test_snake_case_filename_tokenizer_avoids_false_positive() -> None:
    mod = load_mod()
    # Filenames use the same tokenizer now (not a raw substring regex), so
    # "holder" inside a snake_case basename does not collide with "old".
    assert mod._has_bad_word("storage_release_holder_test") is False
    assert mod._has_bad_word("tui_phase1_test") is True


def test_check_file_flags_bad_filename() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        d = Path(td)
        f = d / "scratch_helper.go"
        f.write_text("package p\n\nfunc Hello() {}\n", encoding="utf-8")
        violations = mod.check_file(f, f.relative_to(d))
        assert any("filename" in v for v in violations), violations


def test_check_file_flags_bad_func_decl() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        d = Path(td)
        f = d / "helper.go"
        f.write_text(
            "package p\n\nfunc RunOldMigration() {}\n\nfunc (r *R) HoldSelected() {}\n",
            encoding="utf-8",
        )
        violations = mod.check_file(f, f.relative_to(d))
        assert len(violations) == 1, violations
        assert "helper.go:3" in violations[0], violations


def test_check_file_clean_go_passes() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        d = Path(td)
        f = d / "widget.go"
        f.write_text("package p\n\nfunc Hold() {}\n\nfunc Widget2() {}\n", encoding="utf-8")
        violations = mod.check_file(f, f.relative_to(d))
        assert violations == [], violations


def main() -> None:
    test_camel_word_tokenizer_avoids_substring_false_positive()
    test_camel_word_tokenizer_catches_real_hits()
    test_version_suffix_detected()
    test_snake_case_filename_tokenizer_avoids_false_positive()
    test_check_file_flags_bad_filename()
    test_check_file_flags_bad_func_decl()
    test_check_file_clean_go_passes()
    print("test_check_names: ok")


if __name__ == "__main__":
    main()
