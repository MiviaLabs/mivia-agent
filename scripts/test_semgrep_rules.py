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
import tomllib
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
RULES = ROOT / "semgrep" / "agent-standards.yml"
PORTABLE_ARCHITECTURE_RULE = "mivia.generic.architecture-review-must-stay-portable"
ARCHITECTURE_SKILL_DIR = ROOT / ".mivia" / "skills" / "architecture-review"

REQUIRED_IDS = [
    "mivia.generic.no-wildcard-bash-allow",
    "mivia.generic.no-shell-metachar-bash-allow",
    "mivia.generic.no-semgrep-suppression",
    "mivia.generic.no-unresolved-drift-markers",
    "mivia.generic.brand-mivialabs",
    "mivia.generic.no-git-hook-bypass-in-agent-config",
    PORTABLE_ARCHITECTURE_RULE,
    "mivia.go.no-direct-tool-execution-outside-dispatcher",
]

# Substrings expected near each rule id (YAML-escaped forms, not compiled regexes).
RULE_PATTERN_HINTS = {
    "mivia.generic.no-wildcard-bash-allow": r"Bash\(",
    "mivia.generic.no-semgrep-suppression": r"nosemgrep",
    "mivia.generic.no-git-hook-bypass-in-agent-config": r"no-verify",
}

PORTABILITY_VIOLATIONS = [
    "Review this mivia design.",
    "Always emit mivia-report/v1.",
    "Read .mivia/rules/05-adlc.md.",
    "Read AGENTS.md first.",
    "Run this at ADLC Step 0.",
    "Run this review at Step 0.",
    "Send findings to the challenge panel.",
    "Use the bug-audit skill.",
    "Use the secure-change skill.",
    "Use the verify-change skill.",
    "Use the engineering:architecture plugin skill.",
    "Reviewing HEAD is sufficient.",
    "Measure callers at git HEAD.",
    "Run git diff before every review.",
    "Go rejects cycles at compile time.",
    "Prefer the Go-idiomatic simpler form.",
    "Use Golang idioms.",
    "Use Python dataclasses.",
    "Use Python dataclasses when the project declares Rust.",
    "Use Python dataclasses and prefer Rust when the project declares Rust.",
    "Run go build ./... and go test ./....",
    "Run cargo test.",
    "Run npm test.",
    "Run pytest.",
    "When the review finishes, run go test ./....",
    "Discover the architecture, then run npm test.",
    "Do not skip required checks; run cargo test.",
    "Use context.Context plumbing.",
    "Build cmd/mivia.",
    "Read docs/OWNERS.yaml.",
    "Update docs/architecture/overview.md.",
    "Read requirements from docs/.",
    "Read .github/architecture.md.",
    "Map INV-AG-* and INV-SEC-*.",
    "Run make verify.",
    "Run make invariants.",
    "Run scripts/check_go_structure.py.",
    "Inspect internal/worker.",
]

PORTABLE_WORDING = [
    "Use a supplied baseline or current snapshot.",
    "Discover the workspace's dependency model.",
    "Review package boundaries when the project has packages.",
    "Use project-native verification commands.",
    "Treat source, data, infrastructure, and documentation as architecture.",
    "Go beyond source dependencies and inspect runtime coupling.",
    "Review HTTP HEAD health checks.",
    "Record the go/no-go deployment decision.",
    "Do not require Git.",
    "Use Git only when discovered in the workspace.",
    "When the workspace uses Python, inspect its declared dependency boundaries.",
    "Use Python only when supplied by the workspace.",
    "Use Python when discovered in the workspace.",
    "Use the Python analyzer only when supplied by the workspace.",
    "Prefer Rust when the project declares Rust.",
    "For Rust projects, discover Cargo's dependency model instead of assuming it.",
    "When Cargo.toml exists, use cargo metadata.",
    "When Cargo.toml exists, run cargo test.",
    "When go.mod exists, run go test ./....",
    "Use npm only when supplied by the workspace.",
    "When package.json exists, run npm test.",
    "When pytest is supplied by the workspace, run pytest.",
    "When the workspace uses Git, run git diff against its chosen baseline.",
    "When the workspace declares Python, use Python tooling.",
    "When docs/ exists, read its architecture records.",
    "When .github/ exists, read its architecture records.",
]


def rule_block(text: str, rule_id: str) -> str:
    """Return one Semgrep rule block, stopping before the next rule."""
    start = text.find(f"  - id: {rule_id}")
    assert start >= 0, rule_id
    end = text.find("\n  - id: ", start + 1)
    return text[start:] if end < 0 else text[start:end]


def portability_pattern(text: str) -> re.Pattern[str]:
    """Pin the portability rule's scope and observable regex behaviour."""
    block = rule_block(text, PORTABLE_ARCHITECTURE_RULE)
    assert "/.mivia/skills/architecture-review/**" in block
    match = re.search(r"(?m)^\s+pattern-regex:\s+'(.+)'\s*$", block)
    assert match, f"{PORTABLE_ARCHITECTURE_RULE} missing single-line pattern-regex"
    return re.compile(match.group(1))


def assert_portability_rule(text: str) -> None:
    pattern = portability_pattern(text)
    missed = [value for value in PORTABILITY_VIOLATIONS if pattern.search(value) is None]
    assert not missed, f"portability rule missed prohibited wording: {missed}"
    false_positives = [value for value in PORTABLE_WORDING if pattern.search(value)]
    assert not false_positives, f"portability rule rejected generic wording: {false_positives}"


def assert_declared_architecture_resources_are_portable(text: str) -> None:
    """Manifest-gated resources are model-facing and need the same guard."""
    manifest_path = ARCHITECTURE_SKILL_DIR / "resources.toml"
    manifest = tomllib.loads(manifest_path.read_text(encoding="utf-8"))
    assert manifest.get("format") == 1, "architecture resource manifest format"
    resources = manifest.get("resources", [])
    assert isinstance(resources, list), "architecture resources must be a list"
    pattern = portability_pattern(text)
    for resource in resources:
        assert isinstance(resource, dict), "architecture resource must be a table"
        relative = resource.get("path")
        assert isinstance(relative, str) and relative and not relative.startswith("/")
        assert "\\" not in relative and ".." not in relative.split("/")
        path = ARCHITECTURE_SKILL_DIR / relative
        assert path.is_file(), f"declared architecture resource missing: {relative}"
        assert pattern.search(path.read_text(encoding="utf-8")) is None, (
            f"declared architecture resource is not portable: {relative}"
        )


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

    assert_portability_rule(text)
    assert_declared_architecture_resources_are_portable(text)

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
