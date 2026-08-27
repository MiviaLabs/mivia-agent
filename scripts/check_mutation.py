#!/usr/bin/env python3
"""Gate/tool: mutation testing for one package at a time.

Applies text-level operator mutations to a package's tracked source,
runs that package's own test target per mutant, and reports the kill
rate against a per-package floor. Ported from mivia-ai-sdk's
scripts/check_mutation.py and adapted to this repo's nested package
layout (internal/<pkg> and cmd/<pkg>, instead of flat top-level
directories) and its `mivia` module path. Stdlib-only plus the go
tool: site-finding shells out to a small embedded go/scanner helper
program, run through `go run`, never a third-party mutation library.
Tokenizer glue lives in scripts/mutation_tokenize.py.

Supersedes the old scripts/mutation_coverage.py stub, which only
checked whether go-mutesting was installed and printed a static
readiness table; it never ran a mutation.
"""
from __future__ import annotations

import argparse
import atexit
import json
import re
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

from mutation_tokenize import MutationError, sites_for_file, sites_from_tokens

# Python's default SIGTERM handling kills the process without running
# `finally` blocks, unlike SIGINT. A killed sweep would then leave a
# mutated file on disk. Re-raising SIGTERM as KeyboardInterrupt routes
# it through the same finally-guaranteed restore path as Ctrl-C.
signal.signal(signal.SIGTERM, signal.default_int_handler)

ROOT = Path(__file__).resolve().parent.parent
POLICY_MUTATION_DIR = ROOT / ".mivia" / "policy" / "mutation"
LEGACY_DENYLIST_DIR = ROOT / "scripts" / "mutation_denylist"
DENYLIST_DIR = POLICY_MUTATION_DIR if POLICY_MUTATION_DIR.exists() else LEGACY_DENYLIST_DIR
TEST_TIMEOUT_SECONDS = 60

# Default sweep targets: the packages backing the invariants listed in
# .mivia/invariants.md, the same set scripts/mutation_coverage.py used
# to report readiness for (minus internal/subagents, which --pkg can
# still name explicitly).
CORE_PACKAGES = [
    "internal/cli",
    "internal/agent",
    "internal/tools",
    "internal/chat",
    "internal/config",
    "internal/hooks",
]

KILLED = "killed"
SURVIVED = "survived"
DISCARDED = "discarded"


def denylist_path(pkg: str, denylist_dir: Path | None = None) -> Path:
    """denylist_path maps a nested package (internal/cli) onto a flat
    JSON filename (internal_cli.json): a package name containing "/"
    cannot be a filename directly."""
    if denylist_dir is not None:
        return denylist_dir / f"{pkg.replace('/', '_')}.json"
    primary = POLICY_MUTATION_DIR / f"{pkg.replace('/', '_')}.json"
    if primary.exists():
        return primary
    legacy = LEGACY_DENYLIST_DIR / f"{pkg.replace('/', '_')}.json"
    if legacy.exists():
        return legacy
    return primary


def load_denylist(pkg: str, denylist_dir: Path | None = None) -> dict:
    """load_denylist reads a package's denylist and floor, or empty defaults."""
    path = denylist_path(pkg, denylist_dir)
    if not path.exists():
        return {"denylist": [], "floor": None}
    data = json.loads(path.read_text())
    data.setdefault("denylist", [])
    data.setdefault("floor", None)
    return data


def denylisted_spans(pkg_dir: Path, denylist: list[dict]) -> dict:
    """denylisted_spans resolves each denylist entry to its one exact
    match span in its named file. Fails loudly on zero or on more
    than one match; a stale or ambiguous entry never rubber-stamps a
    site silently."""
    spans: dict[Path, list[tuple[int, int]]] = {}
    for entry in denylist:
        file_path = pkg_dir / entry["file"]
        text = file_path.read_text()
        snippet = entry["snippet"]
        count = text.count(snippet)
        if count == 0:
            raise MutationError(
                f"denylist entry for {entry['file']}: snippet no longer matches: {snippet!r}"
            )
        if count > 1:
            raise MutationError(
                f"denylist entry for {entry['file']}: snippet matches {count} sites, "
                f"widen it to match one: {snippet!r}"
            )
        start = text.index(snippet)
        spans.setdefault(file_path, []).append((start, start + len(snippet)))
    return spans


def is_denylisted(site, spans: dict) -> bool:
    """is_denylisted reports whether site falls inside a denylisted span."""
    for start, end in spans.get(site.path, []):
        if site.start >= start and site.end <= end:
            return True
    return False


def is_build_tag_gated(path: Path) -> bool:
    """is_build_tag_gated reports a `//go:build` constraint on path.

    A tag-gated file never compiles into the default build the kit
    tests against, so mutating it would always "survive" without
    meaning anything. The kit excludes it.
    """
    for line in path.read_text().splitlines():
        stripped = line.strip()
        if stripped == "":
            continue
        return stripped.startswith("//go:build")
    return False


def source_files(pkg_dir: Path) -> list[Path]:
    """source_files lists a package's mutable files: tracked, non-test,
    and not gated behind a build tag."""
    return sorted(
        p
        for p in pkg_dir.glob("*.go")
        if not p.name.endswith("_test.go") and not is_build_tag_gated(p)
    )


def collect_sites(pkg: str, denylist_dir: Path = DENYLIST_DIR) -> list:
    """collect_sites returns pkg's deterministic mutant list: sorted by
    file, then by byte offset, with denylisted sites already removed."""
    pkg_dir = ROOT / pkg
    data = load_denylist(pkg, denylist_dir)
    spans = denylisted_spans(pkg_dir, data["denylist"])
    sites = []
    for path in source_files(pkg_dir):
        for site in sites_for_file(path):
            if not is_denylisted(site, spans):
                sites.append(site)
    sites.sort(key=lambda s: s.sort_key())
    return sites


def test_target(pkg_dir: Path, pkg: str) -> str:
    """test_target picks the external test directory when it exists
    (<pkg>_test, mirroring Go's external-test-package convention),
    else the package's own directory."""
    ext = pkg_dir / f"{pkg_dir.name}_test"
    if ext.is_dir() and any(ext.glob("*.go")):
        return f"./{pkg}/{pkg_dir.name}_test"
    return f"./{pkg}"


def classify(build_ok: bool, test_outcome: str) -> str:
    """classify maps one mutant's build result and test outcome to a
    verdict. test_outcome is "pass", "fail", or "timeout"."""
    if not build_ok:
        return DISCARDED
    if test_outcome in ("fail", "timeout"):
        return KILLED
    return SURVIVED


def run_mutant(site, original: bytes, pkg: str, pkg_dir: str) -> str:
    """run_mutant applies one mutation, builds, tests, and restores the
    original bytes no matter how the run ends."""
    text = original.decode("utf-8")
    mutated = text[: site.start] + site.new + text[site.end :]
    site.path.write_text(mutated)
    try:
        build = subprocess.run(
            ["go", "build", f"./{pkg}"], cwd=ROOT, capture_output=True, text=True
        )
        if build.returncode != 0:
            print(f"discarded (build failed): {site.path.name}:{site.start} {site.kind}")
            return classify(False, "pass")
        target = test_target(Path(pkg_dir), pkg)
        try:
            test = subprocess.run(
                ["go", "test", target],
                cwd=ROOT,
                capture_output=True,
                text=True,
                timeout=TEST_TIMEOUT_SECONDS,
            )
            outcome = "pass" if test.returncode == 0 else "fail"
        except subprocess.TimeoutExpired:
            outcome = "timeout"
        return classify(True, outcome)
    finally:
        site.path.write_bytes(original)


def sweep(pkg: str, sample: int = None, denylist_dir: Path = DENYLIST_DIR) -> dict:
    """sweep runs every mutant (or the first sample) for pkg and
    returns the kill counts and rate. Every mutated file's original
    bytes are restored, including on interrupt or crash, through the
    finally block below."""
    pkg_dir = ROOT / pkg
    sites = collect_sites(pkg, denylist_dir)
    if sample is not None:
        sites = sites[:sample]
    # Snapshot all target file bytes BEFORE any mutation is applied
    originals: dict[Path, bytes] = {}
    for site in sites:
        if site.path not in originals:
            originals[site.path] = site.path.read_bytes()

    def restore_all() -> None:
        for path, original in originals.items():
            try:
                path.write_bytes(original)
            except Exception:
                pass

    atexit.register(restore_all)
    killed = survived = discarded = 0
    try:
        for site in sites:
            outcome = run_mutant(site, originals[site.path], pkg, str(pkg_dir))
            if outcome == KILLED:
                killed += 1
            elif outcome == SURVIVED:
                survived += 1
                print(
                    f"SURVIVED: {site.path.name}:{site.start} "
                    f"{site.kind} {site.old!r} -> {site.new!r}"
                )
            else:
                discarded += 1
    finally:
        restore_all()
        try:
            atexit.unregister(restore_all)
        except Exception:
            pass
    total = killed + survived
    rate = 100.0 * killed / total if total else 100.0
    return {"killed": killed, "survived": survived, "discarded": discarded, "rate": rate}


def resolve_floor(pkg: str, cli_floor=None, denylist_dir: Path | None = None) -> float | None:
    """resolve_floor honors a CLI --floor for one run only; otherwise it
    reads the package's own stored floor, or None if it has none yet.
    Normalizes fractional floors (e.g. 0.75) to percentages (75.0)."""
    val = cli_floor if cli_floor is not None else load_denylist(pkg, denylist_dir).get("floor")
    if val is not None:
        try:
            f = float(val)
            if 0 < f <= 1.0:
                return f * 100.0
            return f
        except (ValueError, TypeError):
            return None
    return None


def recognized_packages() -> set:
    """recognized_packages lists nested package directories holding
    non-test .go files: internal/<name> and cmd/<name>, this repo's
    two package roots (see AGENTS.md "Layout")."""
    names = set()
    for parent_name in ("internal", "cmd"):
        parent = ROOT / parent_name
        if not parent.is_dir():
            continue
        for d in parent.iterdir():
            if not d.is_dir() or d.name.startswith("."):
                continue
            for f in d.glob("*.go"):
                if not f.name.endswith("_test.go"):
                    names.add(f"{parent_name}/{d.name}")
                    break
    return names


def _probe_planted_site(pkg_dir: Path) -> list[str]:
    """Check: the planted `==` site is generated, and the comment and
    string-literal occurrences of "==" are excluded."""
    planted = pkg_dir / "planted.go"
    planted.write_text(
        'package probepkg\n\n'
        '// a == b in a comment must never become a site\n'
        'const s = "a == b"\n\n'
        'func f(a, b int) bool {\n'
        '\tif a == b {\n'
        '\t\treturn true\n'
        '\t}\n'
        '\treturn false\n'
        '}\n'
    )
    eq_sites = [s for s in sites_for_file(planted) if s.kind == "=="]
    if len(eq_sites) != 1:
        return [f"planted site: want 1 '==' candidate, got {len(eq_sites)}"]
    return []


def _probe_build_tag_exclusion(pkg_dir: Path) -> list[str]:
    """Check: a `//go:build` gated file is excluded from source_files,
    even though it holds an obvious mutation candidate."""
    tagged = pkg_dir / "tagged.go"
    tagged.write_text(
        '//go:build sometag\n\n'
        'package probepkg\n\n'
        'func h(a, b int) bool {\n'
        '\treturn a == b\n'
        '}\n'
    )
    if tagged in source_files(pkg_dir):
        return ["build-tag exclusion: a //go:build file was not excluded"]
    return []


def _probe_denylist(pkg_dir: Path) -> list[str]:
    """Checks: a denylisted site is skipped once its snippet is loaded;
    a missing or ambiguous snippet fails loudly instead of silently
    matching zero or more than one site."""
    problems = []
    denylisted = pkg_dir / "denylisted.go"
    denylisted.write_text(
        'package probepkg\n\n'
        'func g(x, y int) bool {\n'
        '\treturn x == y\n'
        '}\n'
    )
    entries = [{"file": "denylisted.go", "snippet": "x == y"}]
    spans = denylisted_spans(pkg_dir, entries)
    remaining = [s for s in sites_for_file(denylisted) if not is_denylisted(s, spans)]
    if remaining:
        problems.append("denylisted site: still present after filtering")

    try:
        denylisted_spans(pkg_dir, [{"file": "denylisted.go", "snippet": "no such text"}])
        problems.append("denylist: missing snippet did not raise")
    except MutationError:
        pass

    ambiguous = pkg_dir / "ambiguous.go"
    ambiguous.write_text(
        'package probepkg\n\n'
        'func h(a, b int) bool {\n'
        '\t_ = a == b\n'
        '\t_ = a == b\n'
        '\treturn true\n'
        '}\n'
    )
    try:
        denylisted_spans(ambiguous.parent, [{"file": "ambiguous.go", "snippet": "a == b"}])
        problems.append("denylist: ambiguous snippet did not raise")
    except MutationError:
        pass
    return problems


def _probe_not_guard(pkg_dir: Path) -> list[str]:
    """Check: a `!` token immediately followed by `=` is never a
    candidate. Real go/scanner output never produces this adjacency
    (it merges `!=` into one token); this exercises the defensive
    guard itself with a synthetic token list."""
    problems = []
    placeholder = pkg_dir / "planted.go"
    synthetic = [{"pos": 0, "end": 1, "tok": "!"}]
    guarded = sites_from_tokens(synthetic, b"!=", placeholder)
    if guarded:
        problems.append("dropped-! guard: '!' immediately before '=' produced a site")
    unguarded = sites_from_tokens(synthetic, b"! ", placeholder)
    if not unguarded:
        problems.append("dropped-! guard: '!' not before '=' produced no site")
    return problems


def _probe_classify() -> list[str]:
    """Checks: a failed build is discarded; a timeout or a failing test
    counts as killed; a passing test counts as survived."""
    problems = []
    if classify(False, "pass") != DISCARDED:
        problems.append("classify: a failed build must discard the mutant")
    if classify(True, "timeout") != KILLED:
        problems.append("classify: a timed-out test run must count as killed")
    if classify(True, "fail") != KILLED:
        problems.append("classify: a failing test run must count as killed")
    if classify(True, "pass") != SURVIVED:
        problems.append("classify: a passing test run must count as survived")
    return problems


def _probe_test_target(tmp_path: Path) -> list[str]:
    """Check: test-directory selection picks a package's own
    <pkg>_test directory when present, else the package itself."""
    problems = []
    with_ext = tmp_path / "internal" / "withext"
    (with_ext / "withext_test").mkdir(parents=True)
    (with_ext / "withext_test" / "case_test.go").write_text("package withext_test\n")
    got = test_target(with_ext, "internal/withext")
    if got != "./internal/withext/withext_test":
        problems.append(f"test_target: want external dir, got {got}")

    without_ext = tmp_path / "internal" / "noext"
    without_ext.mkdir(parents=True)
    got = test_target(without_ext, "internal/noext")
    if got != "./internal/noext":
        problems.append(f"test_target: want package dir, got {got}")
    return problems


def _probe_floor(tmp_path: Path) -> list[str]:
    """Check: the floor comes from the package's own JSON file when
    --floor is absent; --floor on the CLI overrides it for that run
    only and never writes the file. Also checks the nested-package
    filename mapping (internal/cli -> internal_cli.json)."""
    problems = []
    denylist_dir = tmp_path / "mutation_denylist"
    denylist_dir.mkdir()
    (denylist_dir / "internal_probepkg.json").write_text(
        json.dumps({"denylist": [], "floor": 77})
    )
    pkg = "internal/probepkg"
    if resolve_floor(pkg, None, denylist_dir) != 77:
        problems.append("resolve_floor: did not read the package's stored floor")
    if resolve_floor(pkg, 90, denylist_dir) != 90:
        problems.append("resolve_floor: a CLI floor did not override the stored floor")
    if load_denylist(pkg, denylist_dir)["floor"] != 77:
        problems.append("resolve_floor: a CLI override must not write the package file")
    return problems


def run_probe() -> bool:
    """run_probe exercises the kit's own invariants against planted
    fixtures: no fixture is a checked-in .go file. Returns True on
    success."""
    problems = []
    with tempfile.TemporaryDirectory(prefix="mutation-probe-") as tmp:
        tmp_path = Path(tmp)
        pkg_dir = tmp_path / "probepkg"
        pkg_dir.mkdir()
        problems += _probe_planted_site(pkg_dir)
        problems += _probe_build_tag_exclusion(pkg_dir)
        problems += _probe_denylist(pkg_dir)
        problems += _probe_not_guard(pkg_dir)
        problems += _probe_classify()
        problems += _probe_test_target(tmp_path)
        problems += _probe_floor(tmp_path)

    if problems:
        print("\n".join(problems))
        return False
    return True


def changed_go_files(diff_args: list[str]) -> list[Path]:
    r = subprocess.run(
        ["git", "diff", *diff_args, "--name-only", "--diff-filter=ACMR", "-z", "--", "*.go"],
        cwd=ROOT, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        return []
    return [ROOT / f for f in r.stdout.strip("\0").split("\0") if f and not f.endswith("_test.go")]


def changed_lines_for_file(diff_args: list[str], path: Path) -> set[int]:
    try:
        rel_path = str(path.relative_to(ROOT))
    except ValueError:
        rel_path = str(path)
    r = subprocess.run(
        ["git", "diff", *diff_args, "-U0", "--", rel_path],
        cwd=ROOT, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        return set()
    lines = set()
    for line in r.stdout.splitlines():
        m = re.match(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", line)
        if m:
            start = int(m.group(1))
            count = int(m.group(2)) if m.group(2) is not None else 1
            if count > 0:
                lines.update(range(start, start + count))
    return lines


def sweep_diff(diff_args: list[str]) -> tuple[dict, bool]:
    files = changed_go_files(diff_args)
    if not files:
        print("mutation diff sweep: no changed non-test .go files in scope")
        return {"killed": 0, "survived": 0, "discarded": 0, "rate": 100.0}, False

    pkg_sites: dict[str, list] = {}
    for f in files:
        if is_build_tag_gated(f):
            continue
        try:
            rel = f.relative_to(ROOT)
            pkg = str(rel.parent)
        except ValueError:
            continue

        changed_lines = changed_lines_for_file(diff_args, f)
        if not changed_lines:
            continue

        file_text = f.read_text()
        data = load_denylist(pkg, DENYLIST_DIR)
        spans = denylisted_spans(ROOT / pkg, data.get("denylist", []))

        for site in sites_for_file(f):
            if is_denylisted(site, spans):
                continue
            line_no = file_text.count("\n", 0, site.start) + 1
            if line_no in changed_lines:
                pkg_sites.setdefault(pkg, []).append((site, line_no))

    total_killed = total_survived = total_discarded = 0
    failed = False

    for pkg, sites_with_lines in pkg_sites.items():
        pkg_dir = ROOT / pkg
        originals: dict[Path, bytes] = {}
        try:
            for site, line_no in sites_with_lines:
                if site.path not in originals:
                    originals[site.path] = site.path.read_bytes()
                outcome = run_mutant(site, originals[site.path], pkg, str(pkg_dir))
                if outcome == KILLED:
                    total_killed += 1
                elif outcome == SURVIVED:
                    total_survived += 1
                    failed = True
                    print(
                        f"SURVIVED on diff line: {site.path.name}:{line_no} "
                        f"{site.kind} {site.old!r} -> {site.new!r} (missing test assertion)"
                    )
                else:
                    total_discarded += 1
        finally:
            for path, original in originals.items():
                path.write_bytes(original)

    total = total_killed + total_survived
    rate = 100.0 * total_killed / total if total else 100.0
    print(f"mutation diff sweep: killed={total_killed} survived={total_survived} discarded={total_discarded} rate={rate:.2f}%")
    return {"killed": total_killed, "survived": total_survived, "discarded": total_discarded, "rate": rate}, failed


def main() -> int:
    parser = argparse.ArgumentParser(description="mutation kit: per-package kill rate")
    parser.add_argument("--pkg", help="package directory to mutate, e.g. internal/cli")
    parser.add_argument("--floor", type=float, help="override the stored floor for this run")
    parser.add_argument("--sample", type=int, help="run only the first N mutants")
    parser.add_argument("--probe", action="store_true", help="run the kit's own probe suite")
    parser.add_argument("--staged", action="store_true", help="sweep mutants on staged diff vs HEAD")
    parser.add_argument("--diff", action="store_true", help="sweep mutants on unstaged diff vs HEAD")
    parser.add_argument("--base", help="Base git ref for diff mutation sweep")
    parser.add_argument("--tip", default="HEAD", help="Tip git ref for diff mutation sweep")
    parser.add_argument(
        "--check-floors",
        action="store_true",
        help="sweep all packages with configured floors in .mivia/policy/mutation and fail if below floor",
    )
    parser.add_argument(
        "--all-core",
        action="store_true",
        help="sweep every package in CORE_PACKAGES instead of one --pkg",
    )
    args = parser.parse_args()

    if args.probe:
        return 0 if run_probe() else 1

    if args.check_floors:
        configured = []
        known_pkgs = recognized_packages()
        if POLICY_MUTATION_DIR.is_dir():
            for p in POLICY_MUTATION_DIR.glob("*.json"):
                try:
                    data = json.loads(p.read_text(encoding="utf-8"))
                    if data.get("floor") is not None:
                        pkg = data.get("package")
                        if not pkg:
                            stem = p.stem
                            for kp in known_pkgs:
                                if kp.replace("/", "_") == stem:
                                    pkg = kp
                                    break
                        if not pkg:
                            pkg = p.stem.replace("_", "/")
                        configured.append(pkg)
                except Exception:
                    continue
        if not configured:
            print("check_mutation: no package floors configured in .mivia/policy/mutation")
            return 0
        failed = False
        for pkg in sorted(configured):
            if pkg not in known_pkgs or not (ROOT / pkg).is_dir():
                print(f"check_mutation: invalid or missing package directory for {pkg}", file=sys.stderr)
                failed = True
                continue
            try:
                result = sweep(pkg, args.sample)
            except MutationError as exc:
                print(str(exc))
                return 1
            floor = resolve_floor(pkg, None)
            print(
                f"{pkg}: killed={result['killed']} survived={result['survived']} "
                f"discarded={result['discarded']} rate={result['rate']:.2f}%"
            )
            if floor is not None and result["rate"] < floor:
                print(f"{pkg}: kill rate {result['rate']:.2f}% below the {floor:.2f}% floor")
                failed = True
        return 1 if failed else 0

    if args.staged:
        _, failed = sweep_diff(["--cached"])
        return 1 if failed else 0

    if args.diff:
        _, failed = sweep_diff(["HEAD"])
        return 1 if failed else 0

    if args.base:
        _, failed = sweep_diff([f"{args.base}..{args.tip}"])
        return 1 if failed else 0

    targets = CORE_PACKAGES if args.all_core else ([args.pkg] if args.pkg else None)
    if not targets:
        print("--pkg, --all-core, --check-floors, --staged, or --diff is required unless --probe is set")
        return 2

    known = recognized_packages()
    for pkg in targets:
        if pkg not in known:
            print(f"unrecognized package: {pkg}")
            return 2

    failed = False
    for pkg in targets:
        try:
            result = sweep(pkg, args.sample)
        except MutationError as exc:
            print(str(exc))
            return 1

        floor = resolve_floor(pkg, args.floor)
        print(
            f"{pkg}: killed={result['killed']} survived={result['survived']} "
            f"discarded={result['discarded']} rate={result['rate']:.2f}%"
        )
        if floor is None:
            print(f"{pkg}: no floor set on the CLI or in its denylist file; exploratory run")
            continue
        if result["rate"] < floor:
            print(f"{pkg}: kill rate {result['rate']:.2f}% below the {floor}% floor")
            failed = True

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
