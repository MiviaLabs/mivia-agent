#!/usr/bin/env python3
"""Contract tests for scripts/check_gate_scripts.py.

Every case runs against a fixture tree. The gate reads the Makefile, the hooks
and scripts/*.py, so a probe in the real tree would be a probe in the gate set.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
GATE = ROOT / "scripts" / "check_gate_scripts.py"

RUNNABLE = '''#!/usr/bin/env python3
def main() -> None:
    print("probe: ok")


if __name__ == "__main__":
    main()
'''

NO_GUARD = '''#!/usr/bin/env python3
def main() -> None:
    print("probe: ok")
'''

# No guard AND no main(): only the invoked-script rule can reject this, so a
# test using it cannot be satisfied by the uninvoked-main() rule instead.
NO_GUARD_NO_MAIN = '''#!/usr/bin/env python3
print("probe: ok")
'''

HOOK_GATE = '''#!/usr/bin/env python3
def main() -> None:
    print("hook probe: ok")
'''


def load_gate():
    if str(GATE.parent) not in sys.path:
        sys.path.insert(0, str(GATE.parent))
    spec = importlib.util.spec_from_file_location("check_gate_scripts", GATE)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load check_gate_scripts.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def build(root: Path, makefile: str, scripts: dict[str, str]) -> Path:
    (root / "scripts").mkdir(parents=True)
    (root / "Makefile").write_text(makefile, encoding="utf-8")
    for name, body in scripts.items():
        (root / "scripts" / name).write_text(body, encoding="utf-8")
    return root


def run_on(makefile: str, scripts: dict[str, str]) -> str | None:
    """Run the gate over a fixture tree. Return the failure text, or None."""
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        root = build(Path(tmp), makefile, scripts)
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_gate_scripts(root)
        except SystemExit:
            return captured.getvalue().strip()
    return None


def expect_rejection(makefile: str, scripts: dict[str, str], expected: str) -> None:
    rejection = run_on(makefile, scripts)
    if rejection is None:
        raise AssertionError(f"gate accepted a tree that should fail: {expected}")
    if expected not in rejection:
        raise AssertionError(f"expected {expected!r}, got:\n{rejection}")


def test_accepts_a_runnable_invoked_gate() -> None:
    rejection = run_on(
        "check:\n\t@python3 scripts/check_probe.py\n",
        {"check_probe.py": RUNNABLE},
    )
    if rejection is not None:
        raise AssertionError(rejection)


def test_rejects_an_invocation_of_a_missing_script() -> None:
    """A Makefile target naming a deleted script runs nothing and says nothing."""
    expect_rejection(
        "check:\n\t@python3 scripts/check_ghost.py\n",
        {"check_probe.py": RUNNABLE},
        "which does not exist",
    )


def test_rejects_an_invoked_script_with_no_guard() -> None:
    """Pins the invoked-script rule specifically.

    The fixture defines no main(), so the uninvoked-main() rule cannot fire in
    its place, and the assertion is on this rule's own wording. With NO_GUARD
    (which does define main()) both rules matched the same substring, and
    deleting the invoked-script rule left the suite green.
    """
    expect_rejection(
        "check:\n\t@python3 scripts/check_probe.py\n",
        {"check_probe.py": NO_GUARD_NO_MAIN},
        "runs the imports",
    )


def test_rejects_an_uninvoked_script_with_no_guard() -> None:
    """The invoked set is not the whole set.

    verify_skill_tree.py is imported rather than invoked, and it is the script
    whose missing entry point prompted this gate. A rule keyed only on
    invocation misses it, which is the proxy-for-the-property defect the gate
    exists to stop. The first draft of this gate had exactly that hole, and
    this case is what exposed it.
    """
    expect_rejection(
        "check:\n\t@python3 scripts/check_probe.py\n",
        {"check_probe.py": RUNNABLE, "verify_helper.py": NO_GUARD},
        "verify_helper.py defines main()",
    )


def test_rejects_a_gate_that_takes_the_default_failure_prefix() -> None:
    """A borrowed failure path must report under the borrower's own name."""
    borrower = (
        "from verify_common import ROOT, fail, rel_to_root\n\n\n"
        "def main() -> None:\n    fail('x')\n\n\n"
        'if __name__ == "__main__":\n    main()\n'
    )
    expect_rejection(
        "check:\n\t@python3 scripts/check_probe.py\n",
        {"check_probe.py": RUNNABLE, "check_borrower.py": borrower},
        "name a different script",
    )


def test_rejects_a_binding_that_names_another_script() -> None:
    """The wrong prefix, not only the missing one.

    Every borrower in the tree imports `fail as _fail`, so a rule keyed on the
    bare import form never consulted the binding at all and could not see a
    gate that binds a different gate's name. That is this gate committing the
    class it exists to stop.
    """
    borrower = (
        "import functools\n"
        "from verify_common import ROOT, rel_to_root\n"
        "from verify_common import fail as _fail\n\n"
        'fail = functools.partial(_fail, prefix="verify_agent_config")\n\n\n'
        "def main() -> None:\n    fail('x')\n\n\n"
        'if __name__ == "__main__":\n    main()\n'
    )
    expect_rejection(
        "check:\n\t@python3 scripts/check_probe.py\n",
        {"check_probe.py": RUNNABLE, "check_borrower.py": borrower},
        "name a different script",
    )


def test_covers_a_gate_in_the_hook_directory() -> None:
    """scripts/git-hooks/ holds three python3 gates, two without a .py suffix.

    A sweep over scripts/*.py could not see them, so file-size-check - invoked
    by both pre-commit and pre-push - could lose its entry point with this gate
    green. That is the defect this file exists to stop, one directory over.
    """
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        root = build(Path(tmp), "check:\n\t@python3 scripts/check_probe.py\n",
                     {"check_probe.py": RUNNABLE})
        hooks = root / "scripts" / "git-hooks"
        hooks.mkdir()
        (hooks / "pre-push").write_text(
            "#!/usr/bin/env bash\npython3 \"$ROOT/scripts/git-hooks/size-check\"\n",
            encoding="utf-8")
        (hooks / "size-check").write_text(HOOK_GATE, encoding="utf-8")
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_gate_scripts(root)
        except SystemExit:
            if "scripts/git-hooks/size-check" not in captured.getvalue():
                raise AssertionError(captured.getvalue())
            return
    raise AssertionError("gate accepted an extension-less hook gate with no entry point")


def test_rejects_a_dollar_var_invocation_of_a_missing_script() -> None:
    """Hooks invoke gates as "$ROOT/scripts/x.py"; the prefix must not hide it."""
    mod = load_gate()
    with tempfile.TemporaryDirectory() as tmp:
        root = build(Path(tmp), "check:\n\t@python3 scripts/check_probe.py\n",
                     {"check_probe.py": RUNNABLE})
        hooks = root / "scripts" / "git-hooks"
        hooks.mkdir()
        (hooks / "pre-push").write_text(
            '#!/usr/bin/env bash\npython3 "$ROOT/scripts/check_ghost.py"\n', encoding="utf-8")
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                mod.check_gate_scripts(root)
        except SystemExit:
            if "does not exist" not in captured.getvalue():
                raise AssertionError(captured.getvalue())
            return
    raise AssertionError("gate accepted a $ROOT-form invocation of a missing script")


def test_rejects_a_borrower_that_reaches_fail_through_the_module() -> None:
    """`import verify_common` then `verify_common.fail` is the same defect."""
    borrower = (
        "import verify_common\n"
        "from verify_common import ROOT, rel_to_root\n\n"
        "fail = verify_common.fail\n\n\n"
        "def main() -> None:\n    fail('x')\n\n\n"
        'if __name__ == "__main__":\n    main()\n'
    )
    expect_rejection(
        "check:\n\t@python3 scripts/check_probe.py\n",
        {"check_probe.py": RUNNABLE, "check_borrower.py": borrower},
        "name a different script",
    )


def test_a_prefix_string_in_a_comment_does_not_satisfy_the_rule() -> None:
    """The binding must be the partial application, not the text anywhere."""
    borrower = (
        "import functools\n"
        "from verify_common import ROOT, rel_to_root\n"
        "from verify_common import fail as _fail\n\n"
        '# prefix="check_borrower"\n'
        'fail = functools.partial(_fail, prefix="verify_agent_config")\n\n\n'
        "def main() -> None:\n    fail('x')\n\n\n"
        'if __name__ == "__main__":\n    main()\n'
    )
    expect_rejection(
        "check:\n\t@python3 scripts/check_probe.py\n",
        {"check_probe.py": RUNNABLE, "check_borrower.py": borrower},
        "name a different script",
    )


def test_rejects_a_tree_where_the_invocation_pattern_matches_nothing() -> None:
    """The gate must fail closed if it can no longer see any invocation."""
    expect_rejection(
        "check:\n\t@echo nothing\n",
        {"check_probe.py": RUNNABLE},
        "checks nothing",
    )


def main() -> None:
    test_accepts_a_runnable_invoked_gate()
    test_rejects_an_invocation_of_a_missing_script()
    test_rejects_an_invoked_script_with_no_guard()
    test_rejects_an_uninvoked_script_with_no_guard()
    test_rejects_a_gate_that_takes_the_default_failure_prefix()
    test_rejects_a_binding_that_names_another_script()
    test_a_prefix_string_in_a_comment_does_not_satisfy_the_rule()
    test_rejects_a_borrower_that_reaches_fail_through_the_module()
    test_rejects_a_dollar_var_invocation_of_a_missing_script()
    test_covers_a_gate_in_the_hook_directory()
    test_rejects_a_tree_where_the_invocation_pattern_matches_nothing()
    print("test_check_gate_scripts: ok")


if __name__ == "__main__":
    main()
