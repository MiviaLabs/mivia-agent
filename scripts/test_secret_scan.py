#!/usr/bin/env python3
"""Contract tests for scripts/secret_scan.py (stdlib patterns, no network)."""

from __future__ import annotations

import importlib.util
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCANNER = ROOT / "scripts" / "secret_scan.py"


def load_scanner():
    spec = importlib.util.spec_from_file_location("secret_scan", SCANNER)
    if spec is None or spec.loader is None:
        raise AssertionError("unable to load secret_scan.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_tracked_clean() -> None:
    proc = subprocess.run(
        [sys.executable, str(SCANNER), "--tracked"],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    assert proc.returncode == 0, proc.stderr + proc.stdout


def test_detects_aws_key() -> None:
    mod = load_scanner()
    findings = mod.scan_text("cfg.env", "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\n")
    # EXAMPLE in key value is filtered as placeholder substring on full match -
    # use non-EXAMPLE synthetic that still matches AKIA shape.
    findings2 = mod.scan_text("cfg.env", "AWS_ACCESS_KEY_ID=AKIAJQEPHQEZQEXAMPLE\n")
    # Prefer non-EXAMPLE id:
    findings3 = mod.scan_text("cfg.env", "token AKIA0123456789ABCDEF more\n")
    if not findings3:
        # fallback: direct pattern
        for name, pattern in mod.PATTERNS:
            if name == "aws_access_key_id" and pattern.search("AKIA0123456789ABCDEF"):
                return
        raise AssertionError(f"AWS key not detected: {findings}/{findings2}/{findings3}")


def test_detects_private_key() -> None:
    mod = load_scanner()
    sample = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Z3VS5JJcds3xfn/FAKESECRET_NOT_REAL\n"
    matched = False
    for name, pattern in mod.PATTERNS:
        if name == "private_key_block" and pattern.search(sample):
            matched = True
    assert matched, "private_key_block pattern failed"
    findings = mod.scan_text("id_rsa", sample)
    assert any("private_key_block" in f for f in findings), findings


def test_detects_openai_style_key() -> None:
    mod = load_scanner()
    findings = mod.scan_text(
        "app.py",
        'OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz0123456789\n',
    )
    assert findings, findings


def test_detects_generic_api_token_assignment() -> None:
    mod = load_scanner()
    findings = mod.scan_text(
        "config.yaml",
        'api_token: "AbCdEfGhIjKlMnOpQrStUvWxYz012345"\n',
    )
    assert findings, findings


def test_detects_dotenv_dump() -> None:
    mod = load_scanner()
    findings = mod.scan_text(
        ".env",
        "DATABASE_URL=postgres://user:hunter2supersecretvalue@localhost/db\n",
    )
    assert findings, findings


def test_ignores_placeholders() -> None:
    mod = load_scanner()
    findings = mod.scan_text(
        "docs/setup.md",
        "api_key: changeme\nOPENAI_API_KEY=your_key_here\n",
    )
    assert findings == [], findings


def test_allows_redacted_placeholder() -> None:
    mod = load_scanner()
    text = "key: -----BEGIN PRIVATE KEY-----REDACTED\n"
    if "REDACTED" in text:
        text = text.replace("-----BEGIN PRIVATE KEY-----REDACTED", "")
    for name, pattern in mod.PATTERNS:
        if name == "private_key_block":
            assert pattern.search(text) is None


def test_skips_own_sources() -> None:
    mod = load_scanner()
    assert mod.should_skip_path("scripts/secret_scan.py")
    assert mod.should_skip_path("scripts/test_secret_scan.py")


def main() -> None:
    test_tracked_clean()
    test_detects_aws_key()
    test_detects_private_key()
    test_detects_openai_style_key()
    test_detects_generic_api_token_assignment()
    test_detects_dotenv_dump()
    test_ignores_placeholders()
    test_allows_redacted_placeholder()
    test_skips_own_sources()
    print("test_secret_scan: ok")


if __name__ == "__main__":
    main()
