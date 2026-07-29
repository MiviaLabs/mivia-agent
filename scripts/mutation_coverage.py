#!/usr/bin/env python3
"""Explore mutation testing readiness for core packages.

Checks if go-mutesting is installed, reports which packages are critical
for invariant coverage, and suggests mutation test targets.

Usage: python3 scripts/mutation_coverage.py [--install-hint]

Output: per-package mutation readiness score + gap report.
"""

import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# Core packages that back system invariants (from .mivia/invariants.md)
CORE_PACKAGES = [
    ("internal/cli", [
        "TUI / Rendering invariants (INV-TUI-1 through INV-TUI-6)",
        "Bridge drain, poll chain, finishStream, smoke tests",
    ]),
    ("internal/agent", [
        "Agent loop invariants (INV-AG-4, INV-AG-5)",
        "Delegate/MultiStep, tool redaction",
    ]),
    ("internal/tools", [
        "Tool surface invariants (INV-AG-1, INV-AG-2, INV-SEC-1)",
        "OpenAI schema, filesystem preference, secret paths",
    ]),
    ("internal/chat", [
        "Concurrency invariants (INV-AG-3)",
        "Session.SendUser data-race safety",
    ]),
    ("internal/config", [
        "Privacy/security invariants (INV-SEC-2)",
        "RedactToolArgs default-off",
    ]),
    ("internal/subagents", [
        "Subagent dispatch invariants (INV-AG-6)",
        "Multi-step handler tool access",
    ]),
]


def check_mutesting_installed() -> bool:
    """Check if go-mutesting binary is available."""
    return shutil.which("go-mutesting") is not None


def install_hint() -> None:
    """Print installation instructions."""
    print("  Install:  go install github.com/zimmski/go-mutesting/cmd/go-mutesting@latest")
    print("  Verify:   go-mutesting --help")
    print()
    print("  Alternatively, use go-mutesting via nix or Docker.")


def check_package(pkg: str) -> dict:
    """Run basic checks on a package for mutation readiness."""
    result = {
        "path": pkg,
        "has_tests": False,
        "test_count": 0,
        "has_mutation_test": False,
        "mutation_score": "unknown",
    }

    pkg_path = ROOT / pkg
    test_files = list(pkg_path.glob("*_test.go"))
    result["test_count"] = len(test_files)
    result["has_tests"] = len(test_files) > 0

    base_pkg = Path.cwd().name
    go_pkg = f"github.com/MiviaLabs/mivia-agent/{pkg}"

    if check_mutesting_installed() and result["has_tests"]:
        try:
            proc = subprocess.run(
                ["go-mutesting", "--list", go_pkg],
                capture_output=True, text=True, timeout=10,
            )
            mutations = [l for l in proc.stdout.splitlines() if l.strip()]
            result["mutation_score"] = f"{len(mutations)} mutations found"
            result["mutation_detail"] = proc.stdout[:500]
        except (subprocess.TimeoutExpired, OSError):
            result["mutation_score"] = "timeout/error"
    else:
        result["mutation_score"] = "N/A (no tool or tests)"

    return result


def main() -> None:
    has_tool = check_mutesting_installed()

    print("=" * 70)
    print("  Mutation Testing Readiness Report")
    print("=" * 70)
    print()
    print(f"  go-mutesting installed: {'YES' if has_tool else 'NO'}")
    print()

    if not has_tool and "--install-hint" in sys.argv:
        install_hint()
        return

    print(f"  {'Package':<30} {'Tests':>5}  {'Status':<20}")
    print(f"  {'-'*30} {'-'*5}  {'-'*20}")

    total_tests = 0
    for pkg, areas in CORE_PACKAGES:
        info = check_package(pkg)
        status = info["mutation_score"][:20] if info["has_tests"] else "NO TESTS"
        print(f"  {pkg:<30} {info['test_count']:>5}  {status:<20}")
        total_tests += info["test_count"]

    print()
    print(f"  Core invariant packages: {len(CORE_PACKAGES)}")
    print(f"  Total test files:        {total_tests}")
    print()

    if not has_tool:
        print("  SUGGESTION: Install go-mutesting to get actual mutation scores.")
        print("  Run with --install-hint for installation instructions.")
        print()
        print("  Critical targets for mutation testing (prioritized):")
        for i, (pkg, areas) in enumerate(CORE_PACKAGES, 1):
            print(f"    {i}. {pkg}")
            for area in areas:
                print(f"       - {area}")

    print("=" * 70)


if __name__ == "__main__":
    main()
