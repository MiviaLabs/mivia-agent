#!/usr/bin/env python3
"""Contract tests for validate_invariants.py duplicate-id detection."""

from __future__ import annotations

import contextlib
import importlib.util
import io
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TARGET = ROOT / "scripts" / "validate_invariants.py"


def load_module():
    spec = importlib.util.spec_from_file_location("validate_invariants", TARGET)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def reports_duplicate(module, manifest: str) -> bool:
    """True when the checker rejects the manifest.

    The checker prints its diagnosis before exiting; swallow it so an expected
    failure does not look like a real one in gate output.
    """
    try:
        with contextlib.redirect_stdout(io.StringIO()):
            module.check_duplicate_ids(manifest)
    except SystemExit:
        return True
    return False


def test_true_duplicate_is_rejected() -> None:
    m = load_module()
    manifest = "| ID |\n|----|\n| INV-AG-1 | first |\n| INV-AG-1 | second |\n"
    assert reports_duplicate(m, manifest), "duplicate definition row must fail"


def test_inline_citation_is_not_a_definition() -> None:
    """A description that names other ids must not count as defining them.

    This is the false positive that matters: a naive grep over the manifest
    reports duplicates that do not exist, which is worse than no check.
    """
    m = load_module()
    manifest = (
        "| ID |\n|----|\n"
        "| INV-AG-1 | see INV-AG-1 and INV-AG-2 for context |\n"
        "| INV-AG-2 | second |\n"
    )
    assert not reports_duplicate(m, manifest), "inline citation must not count"


def test_cross_reference_table_is_not_a_definition() -> None:
    """Rows under "Liveness Gap Notes" restate ids defined above."""
    m = load_module()
    manifest = (
        "| ID |\n| INV-TUI-2 | definition |\n\n"
        "## Liveness Gap Notes\n\n"
        "| ID |\n| INV-TUI-2 | gap note |\n"
    )
    assert not reports_duplicate(m, manifest), "gap-note row must not count"


def test_real_manifest_has_no_duplicates() -> None:
    m = load_module()
    manifest = (ROOT / ".mivia" / "invariants.md").read_text(encoding="utf-8")
    assert not reports_duplicate(m, manifest), ".mivia/invariants.md has a duplicate id"


def main() -> None:
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
    print("test_validate_invariants: ok")


if __name__ == "__main__":
    main()
