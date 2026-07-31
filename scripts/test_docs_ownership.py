#!/usr/bin/env python3
"""Contract tests for scripts/check_docs_ownership.py."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "check_docs_ownership.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("check_docs_ownership", CHECKER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_docs_ownership.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


SAMPLE_OWNERS = """\
version: 1
topics:
  architecture:
    path: docs/architecture/overview.md
    owner: platform
    description: architecture
  architecture_dir:
    path: docs/architecture/
    owner: platform
    description: architecture directory
  adr:
    path: docs/adr/
    owner: architecture
    description: ADRs
"""


def expect_run_checks_fail(mod, *, needle: str) -> str:
    """Run run_checks expecting SystemExit != 0; return captured stderr."""
    buf = io.StringIO()
    with contextlib.redirect_stderr(buf):
        try:
            mod.run_checks(staged_mode=False)
            raise AssertionError("expected run_checks to fail")
        except SystemExit as exc:
            if exc.code == 0:
                raise AssertionError("expected non-zero exit") from exc
    err = buf.getvalue()
    if needle not in err:
        raise AssertionError(f"expected {needle!r} in stderr, got:\n{err}")
    return err


def test_repo_owners_ok() -> None:
    proc = subprocess.run(
        [sys.executable, str(CHECKER)],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_parse_owners() -> None:
    mod = load_mod()
    topics = mod.parse_owners(SAMPLE_OWNERS)
    assert "architecture" in topics
    assert topics["architecture"]["path"] == "docs/architecture/overview.md"
    assert topics["adr"]["path"] == "docs/adr/"


def test_directory_topic_owns_children() -> None:
    mod = load_mod()
    topics = mod.parse_owners(SAMPLE_OWNERS)
    assert mod.owned_by("docs/adr/0001-language-and-runtime.md", topics) == "adr"
    assert mod.owned_by("docs/architecture/overview.md", topics) == "architecture"
    assert mod.owned_by("docs/architecture/other.md", topics) == "architecture_dir"
    assert mod.owned_by("docs/orphan.md", topics) is None


def test_duplicate_h1_detected(tmp: Path) -> None:
    mod = load_mod()
    docs = tmp / "docs"
    docs.mkdir(parents=True)
    # Directory-owned topic so both files pass ownership; only H1 collides.
    owners = """\
version: 1
topics:
  architecture:
    path: docs/architecture/
    owner: platform
    description: architecture
"""
    (docs / "OWNERS.yaml").write_text(owners, encoding="utf-8")
    (docs / "architecture").mkdir()
    (docs / "architecture" / "overview.md").write_text("# Architecture Overview\n", encoding="utf-8")
    (docs / "architecture" / "other.md").write_text("# Architecture Overview\n", encoding="utf-8")
    (tmp / ".mivia" / "policy").mkdir(parents=True)
    (tmp / ".mivia" / "policy" / "docs-ownership.json").write_text(
        '{"allowlistedUnownedPrefixes":[],"forbiddenParallelRoots":[]}',
        encoding="utf-8",
    )

    mod.ROOT = tmp
    mod.OWNERS = docs / "OWNERS.yaml"
    mod.POLICY = tmp / ".mivia" / "policy" / "docs-ownership.json"
    expect_run_checks_fail(mod, needle="duplicate H1 titles")


def test_unowned_doc_fails(tmp: Path) -> None:
    mod = load_mod()
    docs = tmp / "docs"
    docs.mkdir(parents=True)
    owners = """\
version: 1
topics:
  architecture:
    path: docs/architecture/overview.md
    owner: platform
    description: architecture
"""
    (docs / "OWNERS.yaml").write_text(owners, encoding="utf-8")
    (docs / "architecture").mkdir()
    (docs / "architecture" / "overview.md").write_text("# Architecture Overview\n", encoding="utf-8")
    (docs / "architecture" / "other.md").write_text("# Other Note\n", encoding="utf-8")
    (tmp / ".mivia" / "policy").mkdir(parents=True)
    (tmp / ".mivia" / "policy" / "docs-ownership.json").write_text(
        '{"allowlistedUnownedPrefixes":[],"forbiddenParallelRoots":[]}',
        encoding="utf-8",
    )

    mod.ROOT = tmp
    mod.OWNERS = docs / "OWNERS.yaml"
    mod.POLICY = tmp / ".mivia" / "policy" / "docs-ownership.json"
    err = expect_run_checks_fail(mod, needle="docs without OWNERS entry")
    if "docs/architecture/other.md" not in err:
        raise AssertionError(f"unowned path missing from error:\n{err}")


def test_missing_owners_fails(tmp: Path) -> None:
    mod = load_mod()
    (tmp / "docs").mkdir(parents=True)
    mod.ROOT = tmp
    mod.OWNERS = tmp / "docs" / "OWNERS.yaml"
    mod.POLICY = tmp / "missing-policy.json"
    expect_run_checks_fail(mod, needle="docs/OWNERS.yaml is required")


def main() -> None:
    test_parse_owners()
    test_directory_topic_owns_children()
    test_repo_owners_ok()
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        test_duplicate_h1_detected(base / "dup")
        test_unowned_doc_fails(base / "unowned")
        test_missing_owners_fails(base / "missing")
    print("test_docs_ownership: ok")


if __name__ == "__main__":
    main()
