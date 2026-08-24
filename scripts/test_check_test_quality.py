#!/usr/bin/env python3
"""Contract tests for scripts/check_test_quality.py.

Builds isolated git+Go fixture modules and drives the script to verify:
1. Rejection of empty test bodies
2. Rejection of zero-assertion test functions
3. Rejection of tautological assertions (assert.True(t, true), etc.)
4. Rejection of unreviewed t.Skip in git diff
5. Acceptance of proper assertions (t.Fatalf, t.Errorf, assert.*, helper calls, subtests)
"""
from __future__ import annotations

import os
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "check_test_quality.py"


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
    pkg.mkdir(exist_ok=True)
    policy_dir = root / ".mivia" / "policy"
    policy_dir.mkdir(parents=True, exist_ok=True)
    (policy_dir / "test-skips.json").write_text('{"knownSkips": {}}', encoding="utf-8")


def test_valid_test_passes() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "valid_test.go").write_text(
            """package pkg
import "testing"
func TestProper(t *testing.T) {
    if 1 + 1 != 2 {
        t.Fatal("math broken")
    }
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 0, f"expected success, got {r.stderr}"
        assert "OK" in r.stdout


def test_empty_test_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "empty_test.go").write_text(
            """package pkg
import "testing"
func TestEmpty(t *testing.T) {
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "empty_test" in r.stderr


def test_zero_assertions_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "zero_test.go").write_text(
            """package pkg
import "testing"
func TestZero(t *testing.T) {
    x := 1 + 1
    _ = x
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "zero_assertions" in r.stderr


def test_tautological_assertion_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "taut_test.go").write_text(
            """package pkg
import "testing"
type AssertHelper struct{}
func (a AssertHelper) True(t *testing.T, val bool) {}
var assert AssertHelper
func TestTaut(t *testing.T) {
    assert.True(t, true)
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "tautological_assertion" in r.stderr


def test_unreviewed_skip_in_diff_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "base_test.go").write_text(
            """package pkg
import "testing"
func TestBase(t *testing.T) {
    t.Log("base")
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        git("commit", "-q", "-m", "base", cwd=root)

        (root / "pkg" / "base_test.go").write_text(
            """package pkg
import "testing"
func TestBase(t *testing.T) {
    t.Skip("skipping without policy entry")
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--staged"], cwd=root)
        assert r.returncode == 1, f"expected diff failure, got {r.stdout}"
        assert "unreviewed_test_skip" in r.stderr


def test_allowlisted_skip_in_diff_accepted() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / ".mivia" / "policy" / "test-skips.json").write_text(
            """{
  "knownSkips": {
    "pkg/base_test.go": [
      { "line": 4, "reason": "documented environment issue" }
    ]
  }
}""",
            encoding="utf-8",
        )
        (root / "pkg" / "base_test.go").write_text(
            """package pkg
import "testing"
func TestBase(t *testing.T) {
    t.Log("base")
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        git("commit", "-q", "-m", "base", cwd=root)

        (root / "pkg" / "base_test.go").write_text(
            """package pkg
import "testing"
func TestBase(t *testing.T) {
    t.Skip("documented environment issue")
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--staged"], cwd=root)
        assert r.returncode == 0, f"expected success for allowlisted skip, got {r.stderr}"


def main() -> None:
    print("running test_check_test_quality...")
    test_valid_test_passes()
    test_empty_test_rejected()
    test_zero_assertions_rejected()
    test_tautological_assertion_rejected()
    test_unreviewed_skip_in_diff_rejected()
    test_allowlisted_skip_in_diff_accepted()
    print("test_check_test_quality: ok")


if __name__ == "__main__":
    main()
