#!/usr/bin/env python3
"""Contract tests for scripts/check_test_quality.py.
"""
from __future__ import annotations

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
    pkg.mkdir()
    (pkg / "lib.go").write_text("package pkg\nfunc Add(a, b int) int { return a + b }\n", encoding="utf-8")
    (pkg / "lib_test.go").write_text(
        """package pkg
import "testing"
func TestAdd(t *testing.T) {
    if Add(1, 2) != 3 {
        t.Fatal("fail")
    }
}
""",
        encoding="utf-8",
    )
    git("add", "-A", cwd=root)
    git("commit", "-q", "-m", "baseline", cwd=root)


def test_empty_test_body_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "empty_test.go").write_text(
            """package pkg
import "testing"
func TestEmpty(t *testing.T) {}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "empty_test" in r.stdout


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
        assert "zero_assertions" in r.stdout


def test_setup_helper_passing_t_rejected_as_zero_assertions() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "setup_test.go").write_text(
            """package pkg
import "testing"
func setup(t *testing.T) string {
    return t.TempDir()
}
func TestSetupOnly(t *testing.T) {
    dir := setup(t)
    _ = dir
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure for setup helper, got {r.stdout}"
        assert "zero_assertions" in r.stdout


def test_control_flow_only_rejected_as_zero_assertions() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "ctrl_test.go").write_text(
            """package pkg
import "testing"
func TestControlOnly(t *testing.T) {
    defer func() {}()
    if 1+1 == 2 {
        x := 1
        _ = x
    }
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "zero_assertions" in r.stdout


def test_stdlib_tautological_if_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "taut_if_test.go").write_text(
            """package pkg
import "testing"
func TestStdlibTautology(t *testing.T) {
    got := 1
    if got == got {
        t.Fatal("fail")
    }
    if false {
        t.Fatal("dead")
    }
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "tautological_assertion" in r.stdout


def test_tautological_assertions_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "taut_test.go").write_text(
            """package pkg
import (
    "testing"
    "github.com/stretchr/testify/assert"
)
func TestTautological(t *testing.T) {
    assert.True(t, true)
    assert.Equal(t, "foo", "foo")
    assert.NoError(t, nil)
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "tautological_assertion" in r.stdout


def test_empty_subtest_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "sub_test.go").write_text(
            """package pkg
import "testing"
func TestSub(t *testing.T) {
    t.Run("empty", func(t *testing.T) {})
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected failure, got {r.stdout}"
        assert "empty_subtest" in r.stdout


def test_unreviewed_skip_in_diff_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "skip_test.go").write_text(
            """package pkg
import "testing"
func TestSkipped(t *testing.T) {
    t.Skip("some unreviewed skip")
    if 1 != 2 { t.Fatal("fail") }
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--staged"], cwd=root)
        assert r.returncode == 1, f"expected failure for unreviewed skip, got {r.stdout}"
        assert "unreviewed_test_skip" in r.stdout


def test_same_diff_skip_policy_edit_cannot_bypass() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        policy_dir = root / ".mivia" / "policy"
        policy_dir.mkdir(parents=True, exist_ok=True)
        (policy_dir / "test-skips.json").write_text('{\n  "knownSkips": {}\n}\n', encoding="utf-8")
        git("add", "-A", cwd=root)
        git("commit", "-q", "-m", "add empty policy", cwd=root)

        # Add skip and try to self-allowlist in same staged commit
        (root / "pkg" / "skip_test.go").write_text(
            """package pkg
import "testing"
func TestSkipped(t *testing.T) {
    t.Skip("bypass attempt")
    if 1 != 2 { t.Fatal("fail") }
}
""",
            encoding="utf-8",
        )
        (policy_dir / "test-skips.json").write_text(
            '{\n  "knownSkips": {\n    "pkg/skip_test.go": [{"reason": "bypass attempt"}]\n  }\n}\n',
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--staged"], cwd=root)
        assert r.returncode == 1, f"expected failure for same-diff policy edit, got {r.stdout}"
        assert "unreviewed_test_skip" in r.stdout


def test_fmt_errorf_rejected_as_zero_assertions() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "fmt_test.go").write_text(
            """package pkg
import (
    "fmt"
    "testing"
)
func TestThing(t *testing.T) {
    got := 123
    _ = fmt.Errorf("%v", got)
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected zero_assertions failure for fmt.Errorf, got {r.stdout}"
        assert "zero_assertions" in r.stdout


def test_subtest_skip_in_diff_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "subskip_test.go").write_text(
            """package pkg
import "testing"
func TestSubSkipped(t *testing.T) {
    t.Run("sub", func(st *testing.T) {
        st.Skip("subtest skip bypass attempt")
    })
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--staged"], cwd=root)
        assert r.returncode == 1, f"expected failure for subtest skip, got {r.stdout}"
        assert "unreviewed_test_skip" in r.stdout


def test_subtest_skip_does_not_suppress_zero_assertions() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "subskip_zero_test.go").write_text(
            """package pkg
import "testing"
func TestSubSkippedZero(t *testing.T) {
    t.Run("sub", func(st *testing.T) {
        st.Skip("wip")
    })
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected zero_assertions failure despite subtest skip, got {r.stdout}"
        assert "zero_assertions" in r.stdout


def test_must_bare_call_without_t_rejected_as_zero_assertions() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "must_test.go").write_text(
            """package pkg
import "testing"
func mustBuild() string { return "ok" }
func TestMustBare(t *testing.T) {
    _ = mustBuild()
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--worktree"], cwd=root)
        assert r.returncode == 1, f"expected zero_assertions for bare mustBuild(), got {r.stdout}"
        assert "zero_assertions" in r.stdout


def test_deleted_test_file_does_not_crash_the_gate() -> None:
    """Deleting a whole test file must produce a verdict, not a TypeError.

    The diff names the deleted path, so it lands in the inspector's target set
    while no longer existing on disk. The Go inspector then marshals a nil
    slice as the JSON literal `null`, json.loads turned that into None, and the
    caller iterated it. Reverting a commit that added a test file is enough to
    hit this, which is exactly how it was found.

    The verdict itself is not asserted here - removing tests may legitimately
    be rejected as degradation. What must not happen is a crash instead of a
    verdict.
    """
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        git("rm", "-q", "pkg/lib_test.go", cwd=root)
        r = run_script(["--diff"], cwd=root)
        assert "TypeError" not in r.stderr, f"gate crashed instead of reporting: {r.stderr}"
        assert "Traceback" not in r.stderr, f"gate raised instead of reporting: {r.stderr}"


def test_deleted_test_function_in_diff_rejected() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        # lib_test.go has TestAdd from init_fixture
        (root / "pkg" / "lib_test.go").write_text(
            """package pkg
import "testing"
// TestAdd deleted!
func TestOther(t *testing.T) {
    if 1 != 1 { t.Fatal("fail") }
}
""",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--staged"], cwd=root)
        assert r.returncode == 1, f"expected failure for deleted test, got {r.stdout}"
        assert "deleted_test_function" in r.stdout
        assert "TestAdd" in r.stdout


def test_newly_created_skip_policy_file_fails_closed() -> None:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        policy_dir = root / ".mivia" / "policy"
        policy_dir.mkdir(parents=True, exist_ok=True)

        # Introduce skip and newly created test-skips.json (file never existed at base)
        (root / "pkg" / "skip_test.go").write_text(
            """package pkg
import "testing"
func TestSkipped(t *testing.T) {
    t.Skip("fresh policy bypass attempt")
    if 1 != 2 { t.Fatal("fail") }
}
""",
            encoding="utf-8",
        )
        (policy_dir / "test-skips.json").write_text(
            '{\n  "knownSkips": {\n    "pkg/skip_test.go": [{"reason": "fresh policy bypass attempt"}]\n  }\n}\n',
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        r = run_script(["--staged"], cwd=root)
        assert r.returncode == 1, "newly created skip policy must NOT allow self-approval of skip"
        assert "unreviewed_test_skip" in r.stdout


def main() -> int:
    tests = [
        v
        for k, v in sorted(globals().items())
        if k.startswith("test_") and callable(v)
    ]
    for t in tests:
        t()
    print("test_check_test_quality: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
