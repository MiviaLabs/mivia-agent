#!/usr/bin/env python3
"""High-confidence secret scanner for mivia (stdlib only, no network).

Modes:
  --staged              scan staged index blobs
  --tracked             scan all currently tracked files
  --base REF [--tip T]  scan lines added between base...tip (default tip=HEAD)

Fail closed: any high-confidence match exits 1.
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path, PurePosixPath


ROOT = Path(__file__).resolve().parents[1]

# High-confidence patterns only. Prefer false negatives over fixture thrash.
PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("aws_access_key_id", re.compile(r"(?<![A-Z0-9])AKIA[0-9A-Z]{16}(?![A-Z0-9])")),
    (
        "aws_secret_access_key",
        re.compile(
            r"(?i)(?:aws_secret_access_key|aws_secret_key)\s*[=:]\s*['\"]?"
            r"([A-Za-z0-9/+=]{40})['\"]?"
        ),
    ),
    (
        "private_key_block",
        re.compile(r"-----BEGIN (?:RSA |OPENSSH |EC |DSA |ED25519 )?PRIVATE KEY-----"),
    ),
    ("github_pat", re.compile(r"(?<![A-Za-z0-9_])ghp_[A-Za-z0-9]{36}(?![A-Za-z0-9_])")),
    (
        "github_fine_grained",
        re.compile(r"(?<![A-Za-z0-9_])github_pat_[A-Za-z0-9_]{20,}(?![A-Za-z0-9_])"),
    ),
    ("github_token", re.compile(r"(?<![A-Za-z0-9_])gh[pousr]_[A-Za-z0-9_]{36,}(?![A-Za-z0-9_])")),
    ("slack_token", re.compile(r"(?<![A-Za-z0-9_])xox[baprs]-[A-Za-z0-9-]{10,}(?![A-Za-z0-9_-])")),
    ("stripe_live_key", re.compile(r"(?<![A-Za-z0-9_])sk_live_[A-Za-z0-9]{20,}(?![A-Za-z0-9_])")),
    ("openai_sk", re.compile(r"(?<![A-Za-z0-9_])sk-[A-Za-z0-9]{20,}(?![A-Za-z0-9_])")),
    ("anthropic_key", re.compile(r"(?<![A-Za-z0-9_])sk-ant-[A-Za-z0-9\-_]{20,}(?![A-Za-z0-9_])")),
    (
        "generic_api_key_assign",
        re.compile(
            r"(?i)(?:api[_-]?key|api[_-]?token|access[_-]?token|auth[_-]?token|"
            r"secret[_-]?key|client[_-]?secret)\s*[=:]\s*['\"]?"
            r"([A-Za-z0-9_\-./+=]{24,})['\"]?"
        ),
    ),
    (
        "dotenv_secret_dump",
        re.compile(
            r"(?i)^(?:export\s+)?(?:"
            r"AWS_SECRET_ACCESS_KEY|AWS_ACCESS_KEY_ID|DATABASE_URL|"
            r"POSTGRES_PASSWORD|MYSQL_PASSWORD|REDIS_PASSWORD|"
            r"OPENAI_API_KEY|ANTHROPIC_API_KEY|GITHUB_TOKEN|"
            r"NPM_TOKEN|HF_TOKEN|PRIVATE_KEY"
            r")\s*=\s*\S+"
        ),
    ),
]

SKIP_SUFFIXES = {
    ".png",
    ".jpg",
    ".jpeg",
    ".gif",
    ".webp",
    ".ico",
    ".pdf",
    ".zip",
    ".gz",
    ".tar",
    ".tgz",
    ".woff",
    ".woff2",
    ".ttf",
    ".exe",
    ".dll",
    ".so",
    ".dylib",
    ".wasm",
    ".pyc",
    ".sum",
}
SKIP_BASENAMES = {
    "go.sum",
    "package-lock.json",
    "yarn.lock",
    "pnpm-lock.yaml",
    "Cargo.lock",
    "LICENSE",
}
SKIP_PATH_PARTS = {".git", "node_modules", "vendor", "__pycache__"}
SELF_SKIP = {
    "scripts/secret_scan.py",
    "scripts/test_secret_scan.py",
    "scripts/secret-scan",
}

PLACEHOLDER = re.compile(
    r"(?i)^(changeme|placeholder|your[_-]?|xxx+|todo|example|dummy|"
    r"<.*>|\$\{.*\}|null|none|false|true|0+|test|sample|redacted|"
    r"not[_-]?a[_-]?secret|fake_).*"
)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="mivia high-confidence secret scan")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--staged", action="store_true")
    mode.add_argument("--tracked", action="store_true")
    mode.add_argument("--base", help="trusted base revision for added-line scan")
    parser.add_argument("--tip", default="HEAD", help="tip revision (with --base)")
    return parser.parse_args(argv)


def run_git(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=ROOT,
        text=True,
        encoding="utf-8",
        errors="replace",
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=check,
    )


def should_skip_path(rel: str) -> bool:
    pure = PurePosixPath(rel)
    if pure.name in SKIP_BASENAMES:
        return True
    if pure.suffix.lower() in SKIP_SUFFIXES:
        return True
    if any(part in SKIP_PATH_PARTS for part in pure.parts):
        return True
    if rel in SELF_SKIP:
        return True
    return False


def is_placeholder(value: str) -> bool:
    cleaned = value.strip().strip("'\"")
    if len(cleaned) < 8:
        return True
    if "REDACTED" in cleaned or "EXAMPLE" in cleaned or "FAKE_" in cleaned:
        return True
    if cleaned.startswith("sk-fake"):
        return True
    return PLACEHOLDER.match(cleaned) is not None


def match_line(name: str, pattern: re.Pattern[str], line: str) -> bool:
    m = pattern.search(line)
    if not m:
        return False
    if "REDACTED" in line or "BEGIN PRIVATE KEY-----REDACTED" in line:
        return False
    if name in {
        "aws_secret_access_key",
        "generic_api_key_assign",
        "dotenv_secret_dump",
    }:
        candidate = m.group(1) if m.lastindex else m.group(0)
        if name == "dotenv_secret_dump" and "=" in line:
            candidate = line.split("=", 1)[1].strip().strip("'\"")
        if is_placeholder(candidate):
            return False
        if name == "generic_api_key_assign":
            if candidate.isalpha() and candidate.islower():
                return False
            if len(candidate) < 24:
                return False
    snippet = m.group(0)
    if is_placeholder(snippet):
        return False
    return True


def scan_text(rel: str, text: str) -> list[str]:
    hits: list[str] = []
    if "REDACTED" in text and "BEGIN PRIVATE KEY-----REDACTED" in text:
        text = text.replace("-----BEGIN PRIVATE KEY-----REDACTED", "")
    for line_no, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if stripped.startswith("# secret-scan:ignore") or stripped.startswith("// secret-scan:ignore"):
            continue
        for name, pattern in PATTERNS:
            if match_line(name, pattern, line):
                hits.append(f"{rel}:{line_no}: {name}")
    return hits


def staged_paths() -> list[str]:
    proc = run_git(["diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z"], check=True)
    return [p for p in proc.stdout.split("\0") if p]


def tracked_paths() -> list[str]:
    proc = run_git(["ls-files", "-z"], check=True)
    return [p for p in proc.stdout.split("\0") if p]


def range_paths(base: str, tip: str) -> list[str]:
    proc = run_git(
        ["diff", "--diff-filter=ACMR", "--name-only", "-z", f"{base}...{tip}"],
        check=True,
    )
    return [p for p in proc.stdout.split("\0") if p]


def staged_blob(path: str) -> str | None:
    proc = run_git(["show", f":{path}"], check=False)
    if proc.returncode != 0:
        return None
    if "\0" in proc.stdout[:8192]:
        return None
    return proc.stdout


def added_lines(base: str, tip: str, path: str) -> str | None:
    proc = run_git(
        ["diff", "--no-ext-diff", "--unified=0", f"{base}...{tip}", "--", path],
        check=False,
    )
    if proc.returncode != 0:
        return None
    lines = [
        line[1:]
        for line in proc.stdout.splitlines()
        if line.startswith("+") and not line.startswith("+++")
    ]
    if not lines:
        return ""
    return "\n".join(lines) + "\n"


def read_tracked(path: str) -> str | None:
    full = ROOT / path
    if not full.is_file():
        return None
    try:
        data = full.read_bytes()
    except OSError:
        return None
    if b"\0" in data[:8192]:
        return None
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        return data.decode("utf-8", errors="replace")


def scan_file(path: Path) -> list[str]:
    """Scan a filesystem path relative to ROOT (used by tests)."""
    rel = path.relative_to(ROOT).as_posix() if path.is_absolute() else path.as_posix()
    if should_skip_path(rel):
        return []
    text = read_tracked(rel) if (ROOT / rel).is_file() else None
    if text is None:
        try:
            data = path.read_bytes()
        except OSError as exc:
            return [f"{rel}: unreadable: {exc}"]
        if b"\0" in data[:8192]:
            return []
        text = data.decode("utf-8", errors="replace")
    return scan_text(rel, text)


def collect(args: argparse.Namespace) -> list[str]:
    findings: list[str] = []
    if args.staged:
        for path in staged_paths():
            if should_skip_path(path):
                continue
            text = staged_blob(path)
            if text is None:
                continue
            findings.extend(scan_text(path, text))
        return findings

    if args.tracked:
        for path in tracked_paths():
            if should_skip_path(path):
                continue
            text = read_tracked(path)
            if text is None:
                continue
            findings.extend(scan_text(path, text))
        return findings

    for path in range_paths(args.base, args.tip):
        if should_skip_path(path):
            continue
        text = added_lines(args.base, args.tip, path)
        if text is None or text == "":
            continue
        findings.extend(scan_text(path, text))
    return findings


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        findings = collect(args)
    except subprocess.CalledProcessError as exc:
        print(f"secret_scan: git failed: {exc.stderr or exc}", file=sys.stderr)
        return 1

    mode = "staged" if args.staged else ("tracked" if args.tracked else f"{args.base}...{args.tip}")
    if findings:
        print("secret_scan: potential secrets found:", file=sys.stderr)
        for item in findings:
            print(f"  {item}", file=sys.stderr)
        print("secret_scan: refuse to proceed; remove secrets or rotate, then retry", file=sys.stderr)
        return 1
    print(f"secret_scan: ok ({mode})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
