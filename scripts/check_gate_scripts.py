#!/usr/bin/env python3
"""Hold every invoked gate script to being runnable and self-naming.

A script becomes a gate the moment the Makefile or a Git hook invokes it. Two
defects at that boundary make a gate report success it never produced, and
both shipped in this repository:

1. verify_skill_tree.py defined its checks and no entry point. `python3
   scripts/verify_skill_tree.py` executed the imports, printed nothing and
   exited 0. An operator sent there by a failure saw a script that passes.

2. verify_common.fail hard-coded one gate's name, so every verify_skill_tree
   failure printed "verify_agent_config:" and sent that operator to a third
   script that also passes.

Both are the class this repository keeps paying for: a check that reports on a
proxy rather than the property it asserts. The invocation list is derived from
the Makefile and the hooks, never hand-maintained, because a hand-maintained
list of a discovered set is the same defect one level up.
"""

from __future__ import annotations

import functools
import re
from pathlib import Path

from verify_common import ROOT, rel_to_root
from verify_common import fail as _fail

fail = functools.partial(_fail, prefix="check_gate_scripts")


# Files that invoke gate scripts. A script named here is a gate; a script
# nobody invokes is a helper module and is not held to these rules.
INVOKER_GLOBS = ("Makefile", ".githooks/*", "scripts/git-hooks/*")

# `python3 scripts/x.py`, with or without a staged-tree prefix.
INVOCATION = re.compile(
    r"python3?\s+(?:\"\$STAGED_ROOT\"/|\$STAGED_ROOT/)?(scripts/[A-Za-z0-9_]+\.py)"
)

GUARD = re.compile(r"^if __name__ == [\"']__main__[\"']:", re.M)
DEFINES_MAIN = re.compile(r"^def main\(", re.M)

# The one module allowed to take verify_common.fail's default prefix, because
# the default names it. Derived from the default itself, not chosen by hand.
DEFAULT_PREFIX_OWNER = "verify_agent_config"


def invoked_scripts(root: Path) -> dict[str, set[str]]:
    """Every scripts/*.py the Makefile or a hook invokes, and who invokes it."""
    found: dict[str, set[str]] = {}
    for pattern in INVOKER_GLOBS:
        for invoker in sorted(root.glob(pattern)):
            if not invoker.is_file():
                continue
            body = invoker.read_text(encoding="utf-8", errors="replace")
            for match in INVOCATION.finditer(body):
                found.setdefault(match.group(1), set()).add(rel_to_root(invoker))
    return found


def check_gate_scripts(root: Path) -> None:
    """Every invoked script must exist and must run. Takes root so a test can
    exercise this against a fixture."""
    invoked = invoked_scripts(root)
    if not invoked:
        fail(
            "no gate invocations found in the Makefile or the hooks. The "
            "invocation pattern stopped matching, so this gate checks nothing."
        )
    for rel in sorted(invoked):
        callers = ", ".join(sorted(invoked[rel]))
        path = root / rel
        if not path.is_file():
            fail(f"{callers} invokes {rel}, which does not exist.")
        body = path.read_text(encoding="utf-8")
        if not GUARD.search(body):
            fail(
                f"{rel} has no `if __name__ == \"__main__\":` guard, so the "
                f"invocation in {callers} runs the imports, prints nothing and "
                f"exits 0. A gate that cannot report reads as a pass."
            )

    # The invoked set is not the whole set. verify_skill_tree.py is imported by
    # verify_agent_config.py rather than invoked, and it is the script whose
    # missing entry point prompted this gate. Keying only on invocation would
    # have missed it, which is the proxy-for-the-property defect this gate
    # exists to stop. A main() with no guard is dead code in every case.
    for path in sorted((root / "scripts").glob("*.py")):
        body = path.read_text(encoding="utf-8")
        if DEFINES_MAIN.search(body) and not GUARD.search(body):
            fail(
                f"{rel_to_root(path)} defines main() with no "
                f"`if __name__ == \"__main__\":` guard, so running it prints "
                f"nothing and exits 0."
            )

    # A gate that borrows the shared failure path must report under its own
    # name, or its failures send an operator to a script that passes.
    for path in sorted((root / "scripts").glob("*.py")):
        body = path.read_text(encoding="utf-8")
        if "from verify_common import" not in body:
            continue
        if "fail" not in body:
            continue
        stem = path.stem
        if stem == DEFAULT_PREFIX_OWNER:
            continue
        binds = f'prefix="{stem}"' in body or f"prefix='{stem}'" in body
        uses_bare = re.search(r"^from verify_common import [^\n]*\bfail\b(?!\s+as)",
                              body, re.M)
        if uses_bare and not binds:
            fail(
                f"{rel_to_root(path)} imports verify_common.fail without "
                f"binding prefix=\"{stem}\", so its failures print "
                f"\"{DEFAULT_PREFIX_OWNER}:\" and name a different script."
            )


def main() -> None:
    check_gate_scripts(ROOT)
    print("check_gate_scripts: ok")


if __name__ == "__main__":
    main()
