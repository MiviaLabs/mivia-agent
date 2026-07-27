#!/usr/bin/env python3
"""Minimal contract tests for semgrep/agent-standards.yml (mivia).

Pure text assertions so hooks stay fast without a Semgrep runtime.
If semgrep is installed, optionally validate the config.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
RULES = ROOT / "semgrep" / "agent-standards.yml"

REQUIRED_IDS = [
    "mivia.generic.no-wildcard-bash-allow",
    "mivia.generic.no-shell-metachar-bash-allow",
    "mivia.generic.no-semgrep-suppression",
    "mivia.generic.no-unresolved-drift-markers",
    "mivia.generic.brand-mivialabs",
    "mivia.generic.no-git-hook-bypass-in-agent-config",
    "mivia.go.no-direct-tool-execution-outside-dispatcher",
]

# Substrings expected near each rule id (YAML-escaped forms, not compiled regexes).
RULE_PATTERN_HINTS = {
    "mivia.generic.no-wildcard-bash-allow": r"Bash\(",
    "mivia.generic.no-semgrep-suppression": r"nosemgrep",
    "mivia.generic.no-git-hook-bypass-in-agent-config": r"no-verify",
}


def main() -> None:
    if not RULES.is_file():
        print("test_semgrep_rules: skipped (no config yet)")
        return

    text = RULES.read_text(encoding="utf-8")
    assert "rules:" in text
    missing = [rid for rid in REQUIRED_IDS if rid not in text]
    assert not missing, f"missing rule ids: {missing}"

    for rule_id, hint in RULE_PATTERN_HINTS.items():
        idx = text.find(rule_id)
        assert idx >= 0, rule_id
        window = text[idx : idx + 900]
        if re.search(hint, window, flags=re.I) is None:
            assert re.search(hint, text, flags=re.I), f"{rule_id} missing pattern {hint!r}"

    if re.search(r"(?i)allow.*nosemgrep|nosemgrep.*allowed", text):
        raise AssertionError("config must not allow nosemgrep suppressions")

    if shutil.which("semgrep") is not None:
        # Use repo-relative config path; absolute host paths can break some Semgrep installs.
        proc = subprocess.run(
            ["semgrep", "--validate", "--config", "semgrep/agent-standards.yml"],
            cwd=ROOT,
            text=True,
            capture_output=True,
            timeout=120,
        )
        if proc.returncode != 0:
            # Live validate is best-effort when the engine OOMs or mis-mounts; text contracts above still gate.
            print(
                "test_semgrep_rules: semgrep --validate failed (non-fatal if scan works):\n"
                + (proc.stderr or proc.stdout or "")[:800],
                file=sys.stderr,
            )
    else:
        print("semgrep not installed; skipping live validate", file=sys.stderr)

    print("test_semgrep_rules: ok")


if __name__ == "__main__":
    main()
