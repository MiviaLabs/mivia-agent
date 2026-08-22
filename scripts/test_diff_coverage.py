#!/usr/bin/env python3
"""Contract tests for scripts/diff_coverage.py.

Builds isolated git+Go fixture modules (never touches this repo's own tree)
and drives the script exactly as pre-push / make diff-coverage would.
"""

from __future__ import annotations

import importlib.util
import inspect
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


def test_comment_only_change_inside_uncovered_code_passes() -> None:
    """False positive #1: a comment edit is not a statement.

    A coverage block spans a statement's whole line range, so a comment written
    inside an uncovered multi-line statement used to be reported as an
    uncovered LINE - a failure no test could fix, because there is nothing on
    that line to execute.
    """
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        lib = root / "pkg" / "lib.go"
        lib.write_text(
            lib.read_text(encoding="utf-8")
            + "\nfunc Untested(a, b int) int {\n\treturn sum(\n\t\ta,\n\t\tb,\n\t)\n}\n"
            + "\nfunc sum(a, b int) int { return a + b }\n",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        git("commit", "-q", "-m", "add untested fn", cwd=root)

        # Change ONLY a comment, inside the uncovered statement.
        text = lib.read_text(encoding="utf-8")
        lib.write_text(text.replace("\treturn sum(\n", "\treturn sum(\n\t\t// operands follow\n"), encoding="utf-8")
        git("add", "-A", cwd=root)
        staged = git("diff", "--cached", "--name-only", cwd=root).stdout
        assert "pkg/lib.go" in staged, f"fixture staged nothing: {staged!r}"

        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr


def test_block_comment_inside_uncovered_code_passes() -> None:
    """Same rule for /* */ regions, which span lines a line-wise check misses."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        lib = root / "pkg" / "lib.go"
        lib.write_text(
            lib.read_text(encoding="utf-8")
            + "\nfunc Untested(a, b int) int {\n\treturn sum(\n\t\ta,\n\t\tb,\n\t)\n}\n"
            + "\nfunc sum(a, b int) int { return a + b }\n",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        git("commit", "-q", "-m", "add untested fn", cwd=root)

        text = lib.read_text(encoding="utf-8")
        lib.write_text(
            text.replace("\treturn sum(\n", "\t/*\n\t multi-line note\n\t*/\n\treturn sum(\n"),
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr


def test_statement_added_next_to_a_comment_still_fails() -> None:
    """Guard on the filter above: it must drop comments, not statements."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        lib = root / "pkg" / "lib.go"
        lib.write_text(
            lib.read_text(encoding="utf-8")
            + "\n// explanation\nfunc StillUncovered() int {\n\treturn 9\n}\n",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "pkg/lib.go:" in proc.stdout


def test_declarations_only_file_in_a_covered_package_passes() -> None:
    """False positive #2: a const/var/type file contributes no coverage blocks.

    Treating "no blocks" as "unproven" made every edit to a declarations file -
    a prompt string, an error message table - an unfixable failure.
    """
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        (root / "pkg" / "consts.go").write_text(
            "package pkg\n\nconst Greeting = \"hello\"\n", encoding="utf-8"
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert "no statements" in proc.stdout, proc.stdout


def test_declarations_only_package_passes() -> None:
    """False positive #4: a package of pure declarations emits no counters.

    An interface or constants package appears in NO coverage profile, however
    well tested, because it holds no statement to instrument. Reading that
    absence as "never linked into a tested binary" failed every line of the
    package on a gate no test could satisfy.
    """
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        decl = root / "ports"
        decl.mkdir()
        (decl / "ports.go").write_text(
            "package ports\n\n"
            "// Store is a declaration; there is nothing here to execute.\n"
            "type Store interface {\n\tGet(key string) (string, bool)\n}\n\n"
            "const DefaultWidth = 80\n",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert "no statements" in proc.stdout, proc.stdout


def test_one_statement_in_an_untested_package_still_fails() -> None:
    """Guard on the exemption above: it must key on "no statements", not on
    "package missing from the profile". The same package plus a single
    executable function is checkable again, and untested, so it must fail."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        decl = root / "ports"
        decl.mkdir()
        (decl / "ports.go").write_text(
            "package ports\n\n"
            "type Store interface {\n\tGet(key string) (string, bool)\n}\n\n"
            "const DefaultWidth = 80\n\n"
            "func Widen(n int) int {\n\treturn n + DefaultWidth\n}\n",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "ports/ports.go" in proc.stdout


def test_coverage_profile_run_disables_the_test_cache() -> None:
    """False positive #3: a cached test result replays STALE block coordinates.

    `go test` caches test results, coverage output included, and a replayed
    result carries the block coordinates of the source it was recorded against.
    After an edit shifts line numbers, those stale 0-count blocks land on lines
    that no longer hold statements, and the gate reports phantom uncovered
    lines that no test can fix. Observed in this repo; the profile run must
    therefore never be served from the cache.

    Asserted on the command rather than by fixture: the staleness depends on
    what the cache happens to hold, which is exactly the condition a test
    cannot pin down.
    """
    spec = importlib.util.spec_from_file_location("diff_coverage", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    source = inspect.getsource(module.run_coverage_profile)
    assert '"-count=1"' in source, "coverage profile run must pass -count=1 to bypass the test cache"


def test_supplied_profile_is_used_instead_of_running_the_suite() -> None:
    """`make verify` runs the instrumented suite once and shares the profile.

    The proof has to distinguish "reused the profile" from "ran the suite and
    got the same answer", so the fixture is rigged to disagree: the change is
    genuinely uncovered (the suite would fail it, as
    test_uncovered_changed_line_fails_staged pins), while the supplied profile
    reports the same lines covered. Passing therefore means the supplied
    profile was read and no suite run happened.
    """
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        add_uncovered_function(root)
        git("add", "-A", cwd=root)

        lib = (root / "pkg" / "lib.go").read_text(encoding="utf-8").splitlines()
        start = next(i for i, l in enumerate(lib, 1) if l.startswith("func Uncovered"))
        profile = root / "shared.out"
        profile.write_text(
            "mode: set\n"
            f"fixturemod/pkg/lib.go:{start}.24,{start + 2}.2 1 1\n",
            encoding="utf-8",
        )

        proc = run_script(["--staged", "--profile", str(profile)], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert profile.is_file(), "the caller owns the supplied profile; the gate must not delete it"


def test_missing_supplied_profile_is_a_usage_error() -> None:
    """A profile path that does not exist must fail loudly.

    Silently falling back to running the suite would hide a broken verify-go
    hand-off: the gate would still pass, just after paying the full cost the
    sharing exists to avoid.
    """
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        add_uncovered_function(root)
        git("add", "-A", cwd=root)
        proc = run_script(["--staged", "--profile", str(root / "absent.out")], root)
        assert proc.returncode == 2, proc.stdout + proc.stderr
        assert "does not exist" in proc.stderr


def test_known_uncovered_policy_line_passes_gate() -> None:
    """A line listed in .mivia/policy/diff-coverage.json with a reason is
    reported as accepted (stderr) and does not fail the gate; an unlisted
    uncovered line still fails it."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        init_fixture(root)
        add_uncovered_function(root)
        git("add", "-A", cwd=root)
        # First run without the policy: must fail and name the line.
        proc = run_script(["--staged"], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "pkg/lib.go:" in proc.stdout
        # Write the policy accepting every line the first run reported.
        import re as _re
        uncovered_lines = sorted({
            int(m) for m in _re.findall(r"pkg/lib\.go:(\d+)", proc.stdout)
        })
        assert uncovered_lines, proc.stdout
        policy_dir = root / ".mivia" / "policy"
        policy_dir.mkdir(parents=True)
        (policy_dir / "diff-coverage.json").write_text(
            "{\n"
            '  "description": "fixture",\n'
            '  "knownUncovered": {\n'
            '    "pkg/lib.go": {\n'
            f'      "lines": {uncovered_lines},\n'
            '      "reason": "fixture: branch unreachable by construction"\n'
            "    }\n"
            "  }\n"
            "}\n",
            encoding="utf-8",
        )
        proc = run_script(["--staged"], root)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert "accepted as known-uncovered" in proc.stderr
        assert f"pkg/lib.go:{uncovered_lines[0]}" in proc.stderr
        assert "branch unreachable by construction" in proc.stderr
        # A second uncovered line NOT in the policy must still fail.
        lib = root / "pkg" / "lib.go"
        lib.write_text(
            lib.read_text(encoding="utf-8") + "\nfunc AlsoUncovered() int {\n\treturn 4\n}\n",
            encoding="utf-8",
        )
        git("add", "-A", cwd=root)
        proc = run_script(["--staged"], root)
        assert proc.returncode == 1, proc.stdout + proc.stderr
        assert "AlsoUncovered" not in proc.stdout  # findings are line numbers only
        assert "pkg/lib.go:" in proc.stdout


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
