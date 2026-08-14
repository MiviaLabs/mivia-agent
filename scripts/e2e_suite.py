#!/usr/bin/env python3
"""LIVE TEST ONLY - never run without explicit human approval (see AGENTS.md
"Live e2e test workflow"). Every scenario here pushes real branches and opens
real PRs against MiviaLabs/mivia-agent. This script is a tool the user or the
agent invokes deliberately in a session that has already been asked for a
live e2e check; it must never be wired into make verify, CI, or any
automated/scheduled path, and it never runs itself.

A small, versioned suite of stacked-delivery e2e scenarios, so verifying the
delivery engine end to end does not mean inventing a fresh ad hoc task prompt
every time. Two kinds of scenario:

  - "topology" scenarios drive the REAL feature-delivery workflow with a
    task prompt engineered to force a specific, known chunk-dependency
    shape (independent chunks, a DAG diamond, a wide fan-in, a linear
    chain, a single-package non-stacking run). These exercise the real
    decompose agent, so the exact chunk count/shape is the agent's call
    within the prompt's constraints - useful for breadth, not exact
    determinism.
  - "scripted" scenarios are the checked-in, fully deterministic
    .mivia/workflows/e2e-*.toml workflows (diff-size split, PR-metadata
    repair, chunk-scope-guard repair) - same task every run, same failure
    injected every run.

Usage:
  scripts/e2e_suite.py list
  scripts/e2e_suite.py run <name> [<name> ...]      # launch in background
  scripts/e2e_suite.py run --all                    # launch every scenario in parallel
  scripts/e2e_suite.py status                        # summarize every launched run
  scripts/e2e_suite.py kill <name>|--all             # stop a run's driver process

Every run is independent and backgrounded; run --all launches the whole
suite in parallel with one call. Logs land in .mivia/run-logs/e2e-suite/,
and a manifest at .mivia/run-logs/e2e-suite/manifest.json tracks label,
pid, log path, and start time so `status`/`kill` work after this script
exits.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import signal
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
LOG_DIR = Path(os.environ.get("MIVIA_RUN_LOG_DIR", REPO_ROOT / ".mivia" / "run-logs")) / "e2e-suite"
MANIFEST_PATH = LOG_DIR / "manifest.json"


def _random_suffix() -> str:
    """A short, unique-enough token for scenario task text: two smoke runs
    launched in the same process (or two processes started in the same
    second) must never target the same package name, or a rerun's PR
    collides with whatever a prior run already merged into master."""
    return f"{int(time.time())}{os.getpid() % 10000:04d}"


@dataclass
class TopologyScenario:
    """Drives the real feature-delivery workflow with an engineered task."""

    name: str
    description: str
    task: str

    def command(self) -> list[str]:
        return [
            str(REPO_ROOT / "mivia"), "workflow", "run", "feature-delivery",
            "--allow-publish", "--input", f"task={self.task}",
        ]


@dataclass
class ScriptedScenario:
    """Drives a checked-in, fully deterministic e2e-*.toml workflow."""

    name: str
    description: str
    workflow: str
    task: str
    extra_inputs: dict[str, str] = field(default_factory=dict)

    def command(self) -> list[str]:
        cmd = [str(REPO_ROOT / "mivia"), "workflow", "run", self.workflow,
               "--allow-publish", "--input", f"task={self.task}"]
        for k, v in self.extra_inputs.items():
            cmd += ["--input", f"{k}={v}"]
        return cmd


# --- Topology scenarios --------------------------------------------------
# Each task is engineered so the constraints force a specific, known
# chunk-dependency shape while leaving decompose's own judgment about file
# layout intact - this is breadth testing of the real agent pipeline, not a
# scripted replay. Package names carry _SUITE_SUFFIX (one value per script
# invocation, shared across every scenario in that run): a fixed name would
# collide with whatever a PRIOR launch already merged into master the
# moment this suite runs a second time.

_SUITE_SUFFIX = _random_suffix()

TOPOLOGY_SCENARIOS: list[TopologyScenario] = [
    TopologyScenario(
        name="independent-3",
        description="3 independent leaf packages: no depends_on edges. "
                     "Regression for chunk-scope safety (guardChunkScope) and "
                     "the final integration-run admission (stack_mode=single).",
        task=(
            f"Add three independent, dependency-free leaf utility packages, each in its own "
            f"new directory, each with its own table-driven test file: "
            f"1. internal/runeutil{_SUITE_SUFFIX}: a function CountGraphemesApprox(s string) int that counts "
            f"user-perceived characters approximately (runes, treating combining marks as "
            f"zero-width). "
            f"2. internal/pathutil{_SUITE_SUFFIX}: a function SplitExt(p string) (base, ext string) that splits "
            f"a file path into its base and extension parts. "
            f"3. internal/envutil{_SUITE_SUFFIX}: a function ParseBool(s string, def bool) bool that parses "
            f"common boolean strings (\"1\", \"true\", \"yes\", \"on\" and their negatives, "
            f"case-insensitive), returning def when unrecognized. "
            f"These three packages must not import each other and must not modify any existing "
            f"file outside their own new directory."
        ),
    ),
    TopologyScenario(
        name="dag-diamond",
        description="2 parallel leaves -> 1 chunk depending on both -> 1 chunk depending on "
                     "that. Regression for the merge-order deadlock fix "
                     "(blockedByUnmergedDependent) and the sibling-file scope filter.",
        task=(
            f"Add four small Go packages under internal/dagutil{_SUITE_SUFFIX}/ that form a strict dependency "
            f"chain, each in its own subdirectory with its own table-driven test file: "
            f"1. internal/dagutil{_SUITE_SUFFIX}/setops: function Union(a, b []string) []string returning the "
            f"sorted union of two string slices, no duplicates. No dependency on the other three "
            f"packages below. "
            f"2. internal/dagutil{_SUITE_SUFFIX}/strops: function Dedup(a []string) []string returning a with "
            f"duplicates removed, order preserved. No dependency on the other three packages below. "
            f"3. internal/dagutil{_SUITE_SUFFIX}/combine: function CombineUnique(a, b []string) []string that "
            f"MUST import both internal/dagutil{_SUITE_SUFFIX}/setops and internal/dagutil{_SUITE_SUFFIX}/strops, calling "
            f"Union(a, b) and then Dedup on the result. This package cannot be implemented or "
            f"compiled until setops and strops both exist. "
            f"4. internal/dagutil{_SUITE_SUFFIX}/report: function Report(a, b []string) string that MUST import "
            f"internal/dagutil{_SUITE_SUFFIX}/combine, calls CombineUnique(a, b), and returns a comma-joined "
            f"string of the result. This package cannot be implemented or compiled until combine "
            f"exists. "
            f"Decompose this into chunks that respect this exact dependency chain: setops and "
            f"strops are independent of each other and can be parallel chunks; combine depends on "
            f"both setops and strops; report depends on combine. Do not modify any existing file "
            f"outside these four new directories."
        ),
    ),
    TopologyScenario(
        name="wide-fanin-3",
        description="3 independent leaves that ALL feed one aggregator chunk (3 depends_on "
                     "edges into one chunk, not 2). Stresses merge-order and the "
                     "sibling-file union with more than two dependencies at once - a shape "
                     "the dag-diamond scenario does not cover.",
        task=(
            f"Add four small Go packages under internal/fanin{_SUITE_SUFFIX}/, each in its own subdirectory "
            f"with its own table-driven test file: "
            f"1. internal/fanin{_SUITE_SUFFIX}/red: function Count(s string) int returning the number of times "
            f"the byte 'r' appears in s (case-insensitive). No dependency on the other packages. "
            f"2. internal/fanin{_SUITE_SUFFIX}/green: function Count(s string) int returning the number of "
            f"times the byte 'g' appears in s (case-insensitive). No dependency on the other "
            f"packages. "
            f"3. internal/fanin{_SUITE_SUFFIX}/blue: function Count(s string) int returning the number of times "
            f"the byte 'b' appears in s (case-insensitive). No dependency on the other packages. "
            f"4. internal/fanin{_SUITE_SUFFIX}/summary: function Totals(s string) map[string]int that MUST "
            f"import all three of internal/fanin{_SUITE_SUFFIX}/red, internal/fanin{_SUITE_SUFFIX}/green, and "
            f"internal/fanin{_SUITE_SUFFIX}/blue, calling each package's Count(s) and returning a map with keys "
            f"\"red\", \"green\", \"blue\". This package cannot be implemented or compiled until "
            f"red, green, and blue all exist. "
            f"Decompose this into chunks that respect this exact dependency chain: red, green, "
            f"and blue are mutually independent and can be parallel chunks; summary depends on "
            f"all three of them. Do not modify any existing file outside these four new "
            f"directories."
        ),
    ),
    TopologyScenario(
        name="linear-chain-4",
        description="Strict linear dependency, zero parallelism: A -> B -> C -> D. Regression "
                     "for ordering when there is no fan-out to get wrong - the simplest real "
                     "case a merge-order bug could still break.",
        task=(
            f"Add four small Go packages under internal/chain{_SUITE_SUFFIX}/ that form a strict linear "
            f"dependency chain, each in its own subdirectory with its own table-driven test "
            f"file: "
            f"1. internal/chain{_SUITE_SUFFIX}/stepa: function A(n int) int that returns n + 1. No dependency "
            f"on the other packages. "
            f"2. internal/chain{_SUITE_SUFFIX}/stepb: function B(n int) int that MUST import internal/chain{_SUITE_SUFFIX}/stepa "
            f"and returns stepa.A(n) * 2. Cannot be implemented until stepa exists. "
            f"3. internal/chain{_SUITE_SUFFIX}/stepc: function C(n int) int that MUST import internal/chain{_SUITE_SUFFIX}/stepb "
            f"and returns stepb.B(n) - 3. Cannot be implemented until stepb exists. "
            f"4. internal/chain{_SUITE_SUFFIX}/stepd: function D(n int) int that MUST import internal/chain{_SUITE_SUFFIX}/stepc "
            f"and returns stepc.C(n) squared. Cannot be implemented until stepc exists. "
            f"Decompose this into exactly four chunks in this exact linear dependency order: "
            f"stepa, then stepb (depends on stepa), then stepc (depends on stepb), then stepd "
            f"(depends on stepc). No two chunks may run in parallel. Do not modify any existing "
            f"file outside these four new directories."
        ),
    ),
    TopologyScenario(
        name="single-package",
        description="One tiny package: forces stack_mode=single (the non-stacking path), "
                     "the ONE topology scenario that never exercises the merge queue - "
                     "decompose picks single mode for a task this small, so there is no "
                     "chunk to auto-merge. Do not read this as an auto-merge test; it is a "
                     "cheap sanity check that the plain single-PR path still works "
                     "alongside the stacking changes. Target package name is randomized "
                     "per run so repeated launches never collide with a package a prior "
                     "run already merged into master.",
        task=(
            f"Add one small dependency-free leaf package internal/smoke{_random_suffix()} "
            f"with a single function Unique(in []string) []string that returns the input "
            f"without duplicates, preserving first-seen order, plus one table-driven test "
            f"file. Do not modify any existing file."
        ),
    ),
]


# --- Scripted (checked-in, deterministic) scenarios -----------------------

SCRIPTED_SCENARIOS: list[ScriptedScenario] = [
    ScriptedScenario(
        name="split-oversized-diff",
        description="Checked-in e2e-split-test: implement deliberately never shrinks the "
                     "diff, forcing the host's deterministic split + follow-up PR.",
        workflow="e2e-split-test",
        task="e2e suite: oversized diff auto-split",
    ),
    ScriptedScenario(
        name="pr-metadata-repair",
        description="Checked-in e2e-pr-metadata-test: implement deliberately emits an "
                     "invalid pr_title, proving the commit-subject repair path.",
        workflow="e2e-pr-metadata-test",
        task="e2e suite: pr metadata repair",
    ),
    ScriptedScenario(
        name="scope-escape-repair",
        description="Checked-in e2e-scope-escape-test: implement deliberately writes one "
                     "file outside its declared chunk slice, proving guardChunkScope's "
                     "refusal routes to repair instead of a terminal RefusalError.",
        workflow="e2e-scope-escape-test",
        task="e2e suite: chunk scope guard repair",
        extra_inputs={
            "stack_mode": "chunk", "chunk": "c1", "pr_base": "master", "stack_part": "1/1",
            "chunk_plan": '{"id":"c1","title":"scope smoke","files":["testdata/e2e-smoke/scope-ok.md"]}',
        },
    ),
]

# --- Bug-fix scenarios ------------------------------------------------
# Real bug-fix.toml runs, not scripted: a real hunt over a scope narrowed to
# the areas this session's own history shows are bug-dense, told to stop
# after the FIRST confirmed real bug instead of hunting exhaustively - a
# small, bounded live check rather than an open-ended audit.

BUGFIX_SCENARIOS: list[ScriptedScenario] = [
    ScriptedScenario(
        name="bugfix-delivery-diff",
        description="Real bug-fix.toml run, scoped to internal/workflows/delivery/ "
                     "(this session's most bug-dense area) and told to fix only the "
                     "first confirmed real bug it finds, not to hunt exhaustively.",
        workflow="bug-fix",
        task="Hunt for a real, reachable bug. Stop after the first CONFIRMED bug: fix "
             "it with a regression test and stop there - do not keep hunting for more "
             "in the same run. If no confirmed bug exists after a genuine hunt, say so "
             "and make no change.",
        extra_inputs={"scope": "internal/workflows/delivery/ (the stacked-delivery engine)"},
    ),
    ScriptedScenario(
        name="bugfix-stack-cli-diff",
        description="Real bug-fix.toml run, scoped to internal/cli/stack_*.go and "
                     "internal/cli/workflow_deliver*.go (this session's other bug-dense "
                     "area) and told to fix only the first confirmed real bug it finds.",
        workflow="bug-fix",
        task="Hunt for a real, reachable bug. Stop after the first CONFIRMED bug: fix "
             "it with a regression test and stop there - do not keep hunting for more "
             "in the same run. If no confirmed bug exists after a genuine hunt, say so "
             "and make no change.",
        extra_inputs={"scope": "internal/cli/stack_*.go and internal/cli/workflow_deliver*.go "
                                "(the stacking CLI driver and its delivery entry points)"},
    ),
]

SCENARIOS: dict[str, TopologyScenario | ScriptedScenario] = {
    s.name: s for s in (*TOPOLOGY_SCENARIOS, *SCRIPTED_SCENARIOS, *BUGFIX_SCENARIOS)
}


def mivia_binary() -> str:
    local = REPO_ROOT / "mivia"
    if local.is_file() and os.access(local, os.X_OK):
        return str(local)
    print("e2e_suite: no ./mivia binary; run 'make build' first", file=sys.stderr)
    sys.exit(1)


def load_manifest() -> dict:
    if not MANIFEST_PATH.is_file():
        return {}
    try:
        return json.loads(MANIFEST_PATH.read_text())
    except (json.JSONDecodeError, OSError):
        return {}


def save_manifest(manifest: dict) -> None:
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    MANIFEST_PATH.write_text(json.dumps(manifest, indent=2, sort_keys=True))


def launch(scenario: TopologyScenario | ScriptedScenario) -> dict:
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    log_path = LOG_DIR / f"{scenario.name}.log"
    cmd = scenario.command()
    cmd[0] = mivia_binary()
    # "wb", not append: a relaunch of the same scenario must start a fresh
    # log. scan_status scans by pattern priority across the WHOLE file, not
    # by recency, so a stale terminal marker (run_failed, halted) left over
    # from a PRIOR launch would otherwise outrank a live run's fresh
    # progress forever - a launch is a new attempt, not a continuation.
    with open(log_path, "wb") as log_file:
        proc = subprocess.Popen(
            cmd, cwd=REPO_ROOT, stdout=log_file, stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    return {
        "name": scenario.name,
        "kind": "topology" if isinstance(scenario, TopologyScenario) else "scripted",
        "pid": proc.pid,
        "log": str(log_path),
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
    }


def cmd_list(_: argparse.Namespace) -> None:
    print("topology scenarios (real feature-delivery, engineered task):")
    for s in TOPOLOGY_SCENARIOS:
        print(f"  {s.name:<16} {s.description}")
    print("\nscripted scenarios (checked-in, deterministic e2e-*.toml):")
    for s in SCRIPTED_SCENARIOS:
        print(f"  {s.name:<16} {s.description}")
    print("\nbug-fix scenarios (real bug-fix.toml, scoped, first-finding-only):")
    for s in BUGFIX_SCENARIOS:
        print(f"  {s.name:<16} {s.description}")


def cmd_run(args: argparse.Namespace) -> None:
    names = list(SCENARIOS.keys()) if args.all else args.names
    if not names:
        print("e2e_suite run: name a scenario, or pass --all", file=sys.stderr)
        sys.exit(2)
    unknown = [n for n in names if n not in SCENARIOS]
    if unknown:
        print(f"e2e_suite run: unknown scenario(s): {', '.join(unknown)}", file=sys.stderr)
        print("Run 'e2e_suite.py list' to see the available names.", file=sys.stderr)
        sys.exit(2)
    manifest = load_manifest()
    for name in names:
        entry = launch(SCENARIOS[name])
        manifest[name] = entry
        print(f"started {entry['name']:<16} pid={entry['pid']} log={entry['log']}")
    save_manifest(manifest)


# Terminal/notable markers this suite looks for in a run's log, in priority
# order: the first one found (scanning from the end) determines the status
# line. Keep this list widened, not narrowed - a monitor that only matches
# the happy path stays silent on a crash (see the Monitor tool's own
# guidance: "silence is not success").
_STATUS_PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("invalid-transition", re.compile(r"invalid state transition")),
    ("run_failed", re.compile(r'"Kind":"run_failed"')),
    ("halted", re.compile(r"stack .* halted")),
    ("awaits-grant", re.compile(r"awaits (the publish grant|a human)")),
    ("pr-merged", re.compile(r"PR \d+ merged")),
    ("pr-published", re.compile(r"delivery stage=success detail=PR \d+")),
    ("repairing", re.compile(r"repairing at step")),
    ("delivery_pending", re.compile(r"status=delivery_pending")),
    ("succeeded", re.compile(r"status=succeeded")),
]


def scan_status(log_path: str) -> str:
    path = Path(log_path)
    if not path.is_file():
        return "no-log"
    try:
        text = path.read_text(errors="replace")
    except OSError:
        return "unreadable"
    if not text.strip():
        return "starting"
    for label, pattern in _STATUS_PATTERNS:
        for line in reversed(text.splitlines()):
            if pattern.search(line):
                return label
    return "running"


def pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except (ProcessLookupError, PermissionError):
        return False
    except OSError:
        return False
    return True


def cmd_status(_: argparse.Namespace) -> None:
    manifest = load_manifest()
    if not manifest:
        print("e2e_suite: no runs launched yet (see .mivia/run-logs/e2e-suite/manifest.json)")
        return
    print(f"{'name':<16} {'kind':<10} {'pid':<8} {'alive':<6} {'status':<18} log")
    for name, entry in sorted(manifest.items()):
        alive = pid_alive(entry["pid"])
        status = scan_status(entry["log"])
        print(f"{name:<16} {entry.get('kind', '?'):<10} {entry['pid']:<8} "
              f"{'yes' if alive else 'no':<6} {status:<18} {entry['log']}")


def cmd_kill(args: argparse.Namespace) -> None:
    manifest = load_manifest()
    names = list(manifest.keys()) if args.all else args.names
    if not names:
        print("e2e_suite kill: name a scenario, or pass --all", file=sys.stderr)
        sys.exit(2)
    for name in names:
        entry = manifest.get(name)
        if not entry:
            print(f"{name}: not in manifest, nothing to kill")
            continue
        pid = entry["pid"]
        if not pid_alive(pid):
            print(f"{name}: pid {pid} already stopped")
            continue
        try:
            os.killpg(os.getpgid(pid), signal.SIGTERM)
            print(f"{name}: sent SIGTERM to process group of pid {pid}")
        except ProcessLookupError:
            print(f"{name}: pid {pid} already gone")
        except OSError as exc:
            print(f"{name}: failed to kill pid {pid}: {exc}", file=sys.stderr)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="LIVE TEST ONLY - see AGENTS.md 'Live e2e test workflow'. "
                     "Never invoke this script without the user explicitly asking for a "
                     "live e2e check in this session.",
    )
    sub = parser.add_subparsers(dest="cmd", required=True)

    sub.add_parser("list", help="List every available scenario.").set_defaults(func=cmd_list)

    run_p = sub.add_parser("run", help="Launch one or more scenarios in the background.")
    run_p.add_argument("names", nargs="*", help="Scenario name(s) to launch.")
    run_p.add_argument("--all", action="store_true", help="Launch every scenario in parallel.")
    run_p.set_defaults(func=cmd_run)

    sub.add_parser("status", help="Summarize every launched run's live state.").set_defaults(func=cmd_status)

    kill_p = sub.add_parser("kill", help="Stop a launched run's driver process.")
    kill_p.add_argument("names", nargs="*", help="Scenario name(s) to kill.")
    kill_p.add_argument("--all", action="store_true", help="Kill every launched run.")
    kill_p.set_defaults(func=cmd_kill)

    return parser


def main() -> None:
    args = build_parser().parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
