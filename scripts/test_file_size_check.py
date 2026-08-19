#!/usr/bin/env python3
"""Contract tests for scripts/git-hooks/file-size-check."""

from __future__ import annotations

import importlib.util
import tempfile
from importlib.machinery import SourceFileLoader
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts" / "git-hooks" / "file-size-check"


def load_mod():
    loader = SourceFileLoader("file_size_check", str(CHECKER))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    if spec is None:
        raise AssertionError("unable to load file-size-check")
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


def test_dir_mode_passes_on_small_files() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        (base / "small.txt").write_bytes(b"x" * 1024)
        files = mod.get_dir_files(base)
        assert files == ["small.txt"]
        assert mod.check_file_sizes(files, str(base)) == 0


def test_dir_mode_blocks_oversized_file() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        (base / "big.bin").write_bytes(b"x" * (mod.MAX_BYTES + 1))
        files = mod.get_dir_files(base)
        assert mod.check_file_sizes(files, str(base)) == 1


def test_dir_mode_skips_db_suffix_and_symlinks() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        (base / "big.db").write_bytes(b"x" * (mod.MAX_BYTES + 1))
        target = base / "target.txt"
        target.write_bytes(b"x" * (mod.MAX_BYTES + 1))
        (base / "link.txt").symlink_to(target)
        files = mod.get_dir_files(base)
        # The symlink must never be counted (it would double-charge the
        # target's bytes against a path that has none of its own), and the
        # oversized .db is a skip-by-suffix, not a skip-by-absence.
        assert "link.txt" not in files
        assert "big.db" in files
        assert mod.check_file_sizes(files, str(base)) == 1  # only target.txt


def test_dir_mode_skips_vendor_prefix() -> None:
    mod = load_mod()
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        vendor_dir = base / "vendor"
        vendor_dir.mkdir()
        (vendor_dir / "big.go").write_bytes(b"x" * (mod.MAX_BYTES + 1))
        files = mod.get_dir_files(base)
        assert mod.check_file_sizes(files, str(base)) == 0


def main() -> None:
    test_dir_mode_passes_on_small_files()
    test_dir_mode_blocks_oversized_file()
    test_dir_mode_skips_db_suffix_and_symlinks()
    test_dir_mode_skips_vendor_prefix()
    print("test_file_size_check: ok")


if __name__ == "__main__":
    main()
