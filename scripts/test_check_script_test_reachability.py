#!/usr/bin/env python3
"""Contract tests for check_script_test_reachability.py.

Every case is a runner shape that either hides a test (must FAIL) or is a
legitimate style (must PASS). The hiding shapes are the ones a review
planted against the first version of the gate, which missed all of them.
"""

from __future__ import annotations

import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CHECK = ROOT / "scripts" / "check_script_test_reachability.py"


def run_on(source: str) -> subprocess.CompletedProcess[str]:
    """Write source as a scripts/test_*.py in a scratch repo and check it."""
    with tempfile.TemporaryDirectory() as td:
        scratch = Path(td)
        (scratch / "scripts").mkdir()
        (scratch / "scripts" / "test_case.py").write_text(source, encoding="utf-8")
        return subprocess.run(
            [sys.executable, str(CHECK), "--root", str(scratch)],
            capture_output=True, text=True, check=False,
        )


SCAN_MAIN = '''
def main() -> None:
    tests = [v for k, v in sorted(globals().items())
             if k.startswith("test_") and callable(v)]
    for t in tests:
        t()


if __name__ == "__main__":
    main()
'''


def test_explicit_list_missing_a_test_fails() -> None:
    r = run_on('''
def test_wired() -> None:
    assert True


def test_forgotten() -> None:
    assert True


def main() -> None:
    test_wired()


if __name__ == "__main__":
    main()
''')
    assert r.returncode == 1, r.stdout
    assert "test_forgotten" in r.stderr


def test_definition_below_the_guard_fails() -> None:
    r = run_on(SCAN_MAIN + '''

def test_below_the_guard() -> None:
    assert True
''')
    assert r.returncode == 1, r.stdout
    assert "test_below_the_guard" in r.stderr


def test_mixed_runner_with_unwired_parameterized_test_fails() -> None:
    """The rot the gate exists for: a scan covers zero-arg tests while
    parameterized ones stay on a hand-maintained list, and one is missed."""
    r = run_on('''
from pathlib import Path


def test_zero_arg() -> None:
    assert True


def test_wired_fixture(base: Path) -> None:
    assert base


def test_forgotten_fixture(base: Path) -> None:
    assert base


def main() -> None:
    for k, v in sorted(globals().items()):
        if k.startswith("test_") and callable(v):
            pass
    test_wired_fixture(Path("/tmp"))


if __name__ == "__main__":
    main()
''')
    assert r.returncode == 1, r.stdout
    assert "test_forgotten_fixture" in r.stderr
    assert "test_wired_fixture" not in r.stderr


def test_scan_with_a_name_filter_fails() -> None:
    """A scan that excludes names by list can hide a test silently."""
    r = run_on('''
SKIP = {"test_broken"}


def test_ok() -> None:
    assert True


def test_broken() -> None:
    assert True


def main() -> None:
    for k, v in sorted(globals().items()):
        if k.startswith("test_") and k not in SKIP and callable(v):
            v()


if __name__ == "__main__":
    main()
''')
    assert r.returncode == 1, r.stdout
    assert "filter" in r.stderr.lower()


def test_runner_without_main_is_still_checked() -> None:
    """An inline __main__ scan is a runner too; a test below it is hidden."""
    r = run_on('''
def test_ok() -> None:
    assert True


if __name__ == "__main__":
    for k, v in sorted(globals().items()):
        if k.startswith("test_") and callable(v):
            v()


def test_below_inline_guard() -> None:
    assert True
''')
    assert r.returncode == 1, r.stdout
    assert "test_below_inline_guard" in r.stderr


def test_defaulted_parameter_test_needs_explicit_wiring() -> None:
    """inspect.signature(...).parameters != {} for a defaulted arg, so a
    zero-arg scan skips it at runtime: it must be referenced explicitly."""
    r = run_on('''
def test_defaulted(n: int = 3) -> None:
    assert n
''' + SCAN_MAIN)
    assert r.returncode == 1, r.stdout
    assert "test_defaulted" in r.stderr


def test_async_test_is_not_invisible() -> None:
    r = run_on('''
async def test_async_forgotten() -> None:
    assert True


def main() -> None:
    pass


if __name__ == "__main__":
    main()
''')
    assert r.returncode == 1, r.stdout
    assert "test_async_forgotten" in r.stderr


def test_test_defined_inside_a_conditional_is_seen() -> None:
    r = run_on('''
import sys

if sys.version_info >= (3, 8):
    def test_conditional_forgotten() -> None:
        assert True


def main() -> None:
    pass


if __name__ == "__main__":
    main()
''')
    assert r.returncode == 1, r.stdout
    assert "test_conditional_forgotten" in r.stderr


# --- legitimate shapes that must PASS -------------------------------------


def test_plain_scan_runner_passes() -> None:
    r = run_on('''
def test_a() -> None:
    assert True


def test_b() -> None:
    assert True
''' + SCAN_MAIN)
    assert r.returncode == 0, r.stderr


def test_registry_of_callables_passes() -> None:
    """A module-level list of test functions is a legitimate style."""
    r = run_on('''
def test_a() -> None:
    assert True


def test_b() -> None:
    assert True


TESTS = [test_a, test_b]


def main() -> None:
    for t in TESTS:
        t()


if __name__ == "__main__":
    main()
''')
    assert r.returncode == 0, r.stderr


def test_trailing_non_test_statement_after_guard_passes() -> None:
    """Only test definitions below the guard are unreachable; other
    trailing statements are harmless."""
    r = run_on('''
def test_a() -> None:
    assert True
''' + SCAN_MAIN + '''

__all__ = ["main"]
''')
    assert r.returncode == 0, r.stderr


def test_mixed_runner_fully_wired_passes() -> None:
    r = run_on('''
from pathlib import Path


def test_zero_arg() -> None:
    assert True


def test_fixture(base: Path) -> None:
    assert base


def main() -> None:
    for k, v in sorted(globals().items()):
        if k.startswith("test_") and callable(v):
            pass
    test_fixture(Path("/tmp"))


if __name__ == "__main__":
    main()
''')
    assert r.returncode == 0, r.stderr


def test_non_runner_file_without_tests_passes() -> None:
    r = run_on('def helper() -> int:\n    return 1\n')
    assert r.returncode == 0, r.stderr


def main() -> int:
    tests = [
        v for k, v in sorted(globals().items())
        if k.startswith("test_") and callable(v)
    ]
    for t in tests:
        t()
    print(f"test_check_script_test_reachability: ok ({len(tests)} tests)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
