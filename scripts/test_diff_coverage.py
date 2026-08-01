#!/usr/bin/env python3
"""Contract tests for scripts/diff_coverage.py.

Builds isolated git+Go fixture modules (never touches this repo's own tree)
and drives the script exactly as pre-push / make diff-coverage would.
"""

from __future__ import annotations

import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "diff_coverage.py"


def git(*args: str, cwd: Path) -> subprocess.CompletedProcess[str]:
    r = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, check=False)
    assert r.returncode == 0, f"git {args} failed: {r.stderr}"
    return r


def run_script(args: list[str], cwd: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["python3", str(SCRIPT), *args],
        cwd=cwd, capture_output=True, text=True, check=False,
    )


def init_fixture(root: Path) -> None:
    git("init", "-q", cwd=root)
    git("config", "user.email", "test@example.com", cwd=root)
    git("config", "user.name", "Test", cwd=root)
    (root / "go.mod").write_text("module fixturemod\n\ngo 1.21\n", encoding="utf-8")
    pkg = root / "pkg"
    pkg.mkdir()
    (pkg / "lib.go").write_text(
        "package pkg\n\nfunc Covered() int {\n\treturn 1\n}\n", encoding="utf-8"
    )
    (pkg / "lib_test.go").write_text(
        "package pkg\n\nimport \"testing\"\n\n"
        "func TestCovered(t *testing.T) {\n\tif Covered() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
        encoding="utf-8",
    )
    git("add", "-A", cwd=root)
    git("commit", "-q", "-m", "baseline", cwd=root)


def add_uncovered_function(root: Path) -> None:
    lib = root / "pkg" / "lib.go"
    lib.write_text(
        lib.read_text(encoding="utf-8") + "\nfunc Uncovered() int {\n\treturn 2\n}\n",
        encoding="utf-8",
    )


def add_covering_test(root: Path) -> None:
    test = root / "pkg" / "lib_test.go"
    test.write_text(
        test.read_text(encoding="utf-8")
        + "\nfunc TestUncovered(t *testing.T) {\n\tif Uncovered() != 2 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
        encoding="utf-8",
    )


def test_usage_error_without_scope_flag() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        proc = run_script([], root)
        assert proc.returncode == 2, proc.stdout + proc.stderr
        assert "--staged or --base" in proc.stderr


def test_no_staged_changes_skips_clean() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert "skipping" in proc.stdout


def test_uncovered_changed_line_fails_staged() -> None:
    """Mutation proof (RED half): an added, untested function must fail the gate."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        add_uncovered_function(root)
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "pkg/lib.go:" in proc.stdout


def test_covered_changed_line_passes_staged() -> None:
    """Mutation proof (GREEN half): same change, now with a test exercising it, passes."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        add_uncovered_function(root)
        add_covering_test(root)
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert "covered" in proc.stdout


def test_uncovered_changed_line_fails_base_range() -> None:
    """Same mutation, but exercised via --base/--tip (pre-push style range) instead of --staged."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        baseline_sha = git("rev-parse", "HEAD", cwd=root).stdout.strip()
        add_uncovered_function(root)
        git("add", "-A", cwd=root)
        git("commit", "-q", "-m", "add uncovered fn", cwd=root)
        proc = run_script(["--base", baseline_sha], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "pkg/lib.go:" in proc.stdout


def test_unreferenced_package_still_reported_uncovered() -> None:
    """A new package nothing imports still gets a phantom 0-count profile entry
    from its own no-test binary under -coverpkg=./...; confirm it is not
    silently skipped as 'not a statement line'."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        orphan = root / "pkg2"
        orphan.mkdir()
        (orphan / "orphan.go").write_text(
            "package pkg2\n\nfunc Never() int {\n\treturn 3\n}\n", encoding="utf-8"
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "pkg2/orphan.go" in proc.stdout


def test_build_excluded_file_with_no_profile_entry_is_flagged() -> None:
    """A file excluded from every build (all Go files build-constrained out)
    never appears in the coverage profile at all; the file-level fallback
    must still flag it rather than silently pass it as non-statement lines."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        excluded_dir = root / "pkg3"
        excluded_dir.mkdir()
        (excluded_dir / "excluded.go").write_text(
            "//go:build ignore\n\npackage pkg3\n\nfunc NeverBuilt() int {\n\treturn 4\n}\n",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "pkg3/excluded.go" in proc.stdout
        assert "no coverage data" in proc.stdout


def test_test_file_only_changes_are_excluded_from_scope() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        test = root / "pkg" / "lib_test.go"
        test.write_text(test.read_text(encoding="utf-8") + "\n// a harmless comment\n", encoding="utf-8")
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert "skipping" in proc.stdout


if __name__ == "__main__":
    failures = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS {name}")
            except AssertionError as exc:
                failures += 1
                print(f"FAIL {name}: {exc}")
    if failures:
        raise SystemExit(f"{failures} test(s) failed")
    print("All diff_coverage contract tests passed")
