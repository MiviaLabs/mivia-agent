"""Tests for check-commit-subject."""
from __future__ import annotations

import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent
SCRIPT = ROOT / "scripts" / "git-hooks" / "check-commit-subject"
POLICY = ROOT / ".mivia" / "policy" / "commit-message.json"


def check(subject: str) -> tuple[int, str]:
    """Run check-commit-subject and return (exit_code, stderr)."""
    result = subprocess.run(
        [sys.executable, str(SCRIPT), subject],
        capture_output=True, text=True,
    )
    return result.returncode, result.stderr


class TestCheckCommitSubject(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not SCRIPT.exists():
            raise unittest.SkipTest(f"{SCRIPT} not found")
        if not POLICY.exists():
            raise unittest.SkipTest(f"{POLICY} not found")
        with open(POLICY) as f:
            cls.policy = json.load(f)
        cls.scopes = set(cls.policy["scopes"])
        cls.types = set(cls.policy["types"])

    def test_valid_feat(self):
        code, _ = check("feat(cli): add version command")
        self.assertEqual(code, 0)

    def test_valid_fix(self):
        code, _ = check("fix(hooks): print allowed scopes on commit-msg failure")
        self.assertEqual(code, 0)

    def test_valid_docs(self):
        code, _ = check("docs(docs): document commit scopes in contributing")
        self.assertEqual(code, 0)

    def test_valid_chore(self):
        code, _ = check("chore(ai): bootstrap agent control surface")
        self.assertEqual(code, 0)

    def test_valid_security(self):
        code, _ = check("security(quality): tighten secret scan patterns")
        self.assertEqual(code, 0)

    def test_valid_test(self):
        code, _ = check("test(hooks): cover invalid scope error output")
        self.assertEqual(code, 0)

    def test_subject_too_long(self):
        long = "fix(cli): " + "a" * 100  # way over 72
        code, stderr = check(long)
        self.assertEqual(code, 1)
        self.assertIn("max", stderr.lower())

    def test_missing_scope(self):
        code, stderr = check("feat: add version command")
        self.assertEqual(code, 1)
        self.assertIn("scope", stderr.lower())

    def test_invalid_type(self):
        code, stderr = check("wtf(cli): broken commit")
        self.assertEqual(code, 1)
        self.assertIn("type", stderr.lower())

    def test_invalid_scope(self):
        code, stderr = check("feat(bogus): invalid scope")
        self.assertEqual(code, 1)
        self.assertIn("scope", stderr.lower())

    def test_uppercase_body(self):
        code, stderr = check("fix(cli): Uppercase start")
        self.assertEqual(code, 1)

    def test_trailing_period(self):
        code, stderr = check("fix(cli): lowercase subject.")
        self.assertEqual(code, 1)
        self.assertIn("period", stderr.lower())

    def test_empty_subject(self):
        code, stderr = check("")
        self.assertEqual(code, 1)

    def test_scoped_scope_with_slash(self):
        # Some repos use multi-word scopes with /
        code, _ = check("fix(ci): update workflow")
        self.assertEqual(code, 0)

    def test_break_notation(self):
        code, stderr = check("feat(cli)!: breaking change")
        self.assertEqual(code, 0)

    def test_no_colon(self):
        code, stderr = check("feat(cli) add version")
        self.assertEqual(code, 1)
        self.assertIn("colon", stderr.lower())


if __name__ == "__main__":
    unittest.main()
