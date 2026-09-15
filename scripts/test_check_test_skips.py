#!/usr/bin/env python3
"""Contract tests for scripts/check_test_skips.py."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "check_test_skips.py"


def load_mod():
    spec = importlib.util.spec_from_file_location("check_test_skips", CHECKER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_test_skips.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def scratch(policy: str | None, test_file: str | None = None) -> Path:
    td = Path(tempfile.mkdtemp())
    if policy is not None:
        (td / ".mivia" / "policy").mkdir(parents=True)
        (td / ".mivia" / "policy" / "test-skips.json").write_text(
            policy, encoding="utf-8"
        )
    if test_file is not None:
        (td / "pkg").mkdir()
        (td / "pkg" / "x_test.go").write_text(test_file, encoding="utf-8")
    return td


def run(mod, root: Path) -> str | None:
    """Run check_ledger. Returns the captured stderr on failure, else None.

    verify_common.fail prints to stderr and raises a bare SystemExit(1);
    the message never lands in the exception's args.
    """
    err = io.StringIO()
    try:
        with contextlib.redirect_stderr(err):
            mod.check_ledger(root)
    except SystemExit as exc:
        assert exc.code == 1, f"expected exit 1, got {exc.code}"
        return err.getvalue()
    return None


SKIP_AT_10 = "\n" * 9 + '\tt.Skip("reason one")\n'
POLICY_10 = (
    '{"knownSkips": {"pkg/x_test.go": '
    '[{"line": 10, "reason": "reason one"}]}}'
)


def test_valid_ledger_passes() -> None:
    mod = load_mod()
    assert run(mod, scratch(POLICY_10, SKIP_AT_10)) is None


def test_live_skip_without_entry_passes() -> None:
    # One-directional by design: environmental guards need no entry.
    mod = load_mod()
    assert run(mod, scratch(POLICY_10, SKIP_AT_10 + '\tt.Skip("unrecorded")\n')) is None


def test_missing_policy_fails() -> None:
    mod = load_mod()
    exc = run(mod, scratch(None))
    assert exc is not None and "does not exist" in exc


def test_invalid_json_fails() -> None:
    mod = load_mod()
    exc = run(mod, scratch("{not json"))
    assert exc is not None and "not valid JSON" in exc


def test_missing_knownskips_fails() -> None:
    mod = load_mod()
    exc = run(mod, scratch('{"somethingElse": {}}'))
    assert exc is not None and 'no "knownSkips"' in exc


def test_empty_ledger_fails() -> None:
    mod = load_mod()
    exc = run(mod, scratch('{"knownSkips": {}}'))
    assert exc is not None and "empty" in exc


def test_dead_entry_fails() -> None:
    # The exact defect this gate exists for: the skip was removed but the
    # ledger entry stayed behind.
    mod = load_mod()
    exc = run(mod, scratch(POLICY_10, "\n" * 9 + "\tpass\n"))
    assert exc is not None and "no t.Skip call" in exc


def test_drifted_line_fails() -> None:
    # The skip moved one line down; the entry pins the old line. Fails on
    # purpose: the entry describes the wrong line.
    mod = load_mod()
    exc = run(mod, scratch(POLICY_10, "\n" + SKIP_AT_10))
    assert exc is not None and "no t.Skip call" in exc


def test_commented_skip_fails() -> None:
    # A mention of t.Skip in a comment is not a skip call.
    mod = load_mod()
    exc = run(mod, scratch(POLICY_10, "\n" * 9 + "// t.Skip(\"reason one\")\n"))
    assert exc is not None and "no t.Skip call" in exc


def test_out_of_range_line_fails() -> None:
    mod = load_mod()
    policy = (
        '{"knownSkips": {"pkg/x_test.go": '
        '[{"line": 999, "reason": "reason one"}]}}'
    )
    exc = run(mod, scratch(policy, SKIP_AT_10))
    assert exc is not None and "outside the file" in exc


def test_missing_file_fails() -> None:
    mod = load_mod()
    exc = run(mod, scratch(POLICY_10))
    assert exc is not None and "does not exist in the tree" in exc


def test_entry_without_line_fails() -> None:
    mod = load_mod()
    policy = '{"knownSkips": {"pkg/x_test.go": [{"reason": "reason one"}]}}'
    exc = run(mod, scratch(policy, SKIP_AT_10))
    assert exc is not None and 'without a "line"' in exc


def test_file_key_with_no_entries_fails() -> None:
    mod = load_mod()
    policy = '{"knownSkips": {"pkg/x_test.go": []}}'
    exc = run(mod, scratch(policy, SKIP_AT_10))
    assert exc is not None and "maps to no entries" in exc


def test_skipnow_and_skipf_count() -> None:
    mod = load_mod()
    for call in ('t.SkipNow()', 't.Skipf("r %v", x)'):
        body = "\n" * 9 + f"\t{call}\n"
        assert run(mod, scratch(POLICY_10, body)) is None, call


def main() -> None:
    test_valid_ledger_passes()
    test_live_skip_without_entry_passes()
    test_missing_policy_fails()
    test_invalid_json_fails()
    test_missing_knownskips_fails()
    test_empty_ledger_fails()
    test_dead_entry_fails()
    test_drifted_line_fails()
    test_commented_skip_fails()
    test_out_of_range_line_fails()
    test_missing_file_fails()
    test_entry_without_line_fails()
    test_file_key_with_no_entries_fails()
    test_skipnow_and_skipf_count()
    print("test_check_test_skips: ok")


if __name__ == "__main__":
    main()
