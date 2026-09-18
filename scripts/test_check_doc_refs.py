#!/usr/bin/env python3
"""Contract tests for scripts/check_doc_refs.py."""

from __future__ import annotations

import importlib.util
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "check_doc_refs.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("check_doc_refs", CHECKER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_doc_refs.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_dangling_doc_ref_flagged() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        pkg = Path(td) / "internal" / "demo"
        pkg.mkdir(parents=True)
        f = pkg / "demo.go"
        f.write_text(
            "package demo\n\n// See docs/missing.md for details.\n"
            "func Demo() {}\n",
            encoding="utf-8",
        )
        violations = mod.find_dangling_doc_refs(Path(td))
        assert len(violations) == 1, violations
        assert "internal/demo/demo.go:3" in violations[0], violations
        assert "docs/missing.md" in violations[0], violations


def test_existing_doc_ref_passes() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        pkg = Path(td) / "internal" / "demo"
        pkg.mkdir(parents=True)
        (Path(td) / "docs").mkdir()
        (Path(td) / "docs" / "real.md").write_text("Real.\n", encoding="utf-8")
        (pkg / "demo.go").write_text(
            "package demo\n\n// See docs/real.md.\nfunc Demo() {}\n",
            encoding="utf-8",
        )
        assert mod.find_dangling_doc_refs(Path(td)) == []


def test_block_comment_ref_flagged() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        pkg = Path(td) / "cmd" / "mivia"
        pkg.mkdir(parents=True)
        (pkg / "main.go").write_text(
            "package main\n\n/* Plan: .agents/rules/gone.md */\n"
            "func main() {}\n",
            encoding="utf-8",
        )
        violations = mod.find_dangling_doc_refs(Path(td))
        assert len(violations) == 1, violations
        assert "cmd/mivia/main.go:3" in violations[0], violations


def test_docless_package_flagged() -> None:
    mod = load_mod()
    pkg_docs = {"github.com/x/internal/a": "", "github.com/x/internal/b": "Package b."}
    violations = mod.check_pkg_docs(pkg_docs)
    assert len(violations) == 1, violations
    assert "internal/a" in violations[0], violations


def test_plan_label_flagged() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        pkg = Path(td) / "internal" / "demo"
        pkg.mkdir(parents=True)
        f = pkg / "demo.go"
        f.write_text(
            "package demo\n\n// plan D3: fix later\nfunc Demo() {}\n",
            encoding="utf-8",
        )
        matches = mod.find_plan_label_matches(Path(td))
        assert matches == ["internal/demo/demo.go:3"], matches


def test_plan_label_patterns() -> None:
    mod = load_mod()
    hits = ["§4 revisit", "plan D2 later", "Stage 0 done", "Phase 1 next", "B.1 # tag", "round 3 fix"]
    misses = ["stage 0", "phase 12 ok?", "rounding", "§x", "B.1 no hash"]
    for line in hits:
        assert mod.PLAN_LABEL.search(line), line
    for line in misses:
        assert not mod.PLAN_LABEL.search(line), line


def test_baseline_gate_and_shrink() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as td:
        pkg = Path(td) / "internal" / "demo"
        pkg.mkdir(parents=True)
        f = pkg / "demo.go"
        f.write_text(
            "package demo\n\n// plan D3: fix later\nfunc Demo() {}\n",
            encoding="utf-8",
        )
        # Entry present: no violation.
        base = Path(td) / "baseline.txt"
        base.write_text("# header\ninternal/demo/demo.go:3\n", encoding="utf-8")
        assert mod.check_plan_labels(Path(td), base) == []
        # Baseline entry with no live match must fail (shrink-only rule).
        base.write_text(
            "# header\ninternal/demo/demo.go:3\ninternal/demo/demo.go:9\n",
            encoding="utf-8",
        )
        violations = mod.check_plan_labels(Path(td), base)
        assert len(violations) == 1, violations
        assert "internal/demo/demo.go:9" in violations[0], violations
        # Live match missing from baseline must fail.
        base.write_text("# header\n", encoding="utf-8")
        violations = mod.check_plan_labels(Path(td), base)
        assert len(violations) == 1 and "internal/demo/demo.go:3" in violations[0], violations


def main() -> None:
    test_dangling_doc_ref_flagged()
    test_existing_doc_ref_passes()
    test_block_comment_ref_flagged()
    test_docless_package_flagged()
    test_plan_label_flagged()
    test_plan_label_patterns()
    test_baseline_gate_and_shrink()
    print("test_check_doc_refs: ok")


if __name__ == "__main__":
    main()
