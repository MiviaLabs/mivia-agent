#!/usr/bin/env python3
"""Contract tests for scripts/check_seams.py.

Run: python3 scripts/test_check_seams.py
"""

import json
import os
import subprocess
import sys
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "check_seams.py")


def run_gate(root, args=None):
    cmd = [sys.executable, SCRIPT, "--root", root] + (args or [])
    proc = subprocess.run(cmd, capture_output=True, text=True)
    return proc


def write(root, rel, body):
    path = os.path.join(root, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as fh:
        fh.write(body)


BASELINE = {"seams": {}, "chat_workflows_fanout": {}}


class SeamCountTest(unittest.TestCase):
    def test_counts_func_and_error_seams(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/seams.go", """package workflow

var (
\tAFunc func(int) error
\tBHook func(string)
\tCVar  int = 3
)

var DErr error

var EInit error = nil
""")
            write(root, "internal/cli/other/plain.go", "package other\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            # AFunc, BHook, DErr count; EInit has an initializer and CVar is
            # not a seam type.
            self.assertEqual(base["seams"]["internal/cli/workflow"], 3)
            self.assertNotIn("internal/cli/other", base["seams"])

    def test_test_files_do_not_count(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            write(root, "internal/cli/workflow/a_test.go", "package workflow\n\nvar G func()\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["seams"]["internal/cli/workflow"], 1)

    def test_increase_over_baseline_fails(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            write(root, "internal/cli/workflow/b.go", "package workflow\n\nvar H func()\n")
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("internal/cli/workflow", proc.stdout)

    def test_decrease_is_reported_not_failed(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n\nvar G func()\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            os.remove(os.path.join(root, "internal/cli/workflow/a.go"))
            write(root, "internal/cli/workflow/b.go", "package workflow\n\nvar F func()\n\nfunc init() { F = nil }\n")
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 0, proc.stdout)
            self.assertIn("< baseline", proc.stdout)

    def test_unassigned_seam_fails(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            write(root, "internal/cli/workflow/b.go", "package workflow\n\nfunc use() { F() }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("never assigned", proc.stdout)

    def test_assigned_seam_passes(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            write(root, "internal/cli/wiring.go", "package cli\n\nimport \"github.com/MiviaLabs/mivia-agent/internal/cli/workflow\"\n\nfunc init() { workflow.F = func() {} }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 0, proc.stdout)

    def test_tuple_assignment_passes(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar (\n\tF func()\n\tG func(int)\n)\n")
            write(root, "internal/cli/workflow/b.go", "package workflow\n\nfunc wire() (func(), func(int)) { return nil, nil }\n\nfunc init() { F, G = wire() }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 0, proc.stdout)

    def test_multi_seam_packages_keep_individual_bare_assignments(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/c.go", "package workflow\n\nvar G func()\n\nfunc init() { G = nil }\n")
            write(root, "internal/cli/workflow/d.go", "package workflow\n\nvar H func()\n")
            write(root, "internal/cli/workflow/d_test.go", "package workflow\n\nfunc init() { H = nil }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 0, proc.stdout)

    def test_oneline_import_wiring_passes_and_dot_import_fails_closed(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar I func()\n\nvar L func()\n")
            write(root, "internal/cli/other/w1.go", "package other\n\nimport ( \"github.com/MiviaLabs/mivia-agent/internal/cli/workflow\" )\n\nfunc init() { workflow.I = nil }\n")
            # Dot-imported bare assignments cannot be told apart from a
            # same-named local, so they fail closed (no authorization).
            write(root, "internal/cli/other/w2.go", "package other\n\nimport . \"github.com/MiviaLabs/mivia-agent/internal/cli/workflow\"\n\nfunc init() { L = nil }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("workflow.L", proc.stdout)

    def test_chat_fanout_oneline_grouped_import_counts(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/chat/a.go", "package chat\n\nimport ( \"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger\" )\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["chat_workflows_fanout"]["internal/cli/chat"], 1)

    def test_shadowed_alias_local_cannot_clear_seam(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            write(root, "internal/cli/other/c.go", "package other\n\nimport \"github.com/MiviaLabs/mivia-agent/internal/cli/workflow\"\n\nvar _ = workflow.F\n")
            write(root, "internal/cli/other/b.go", "package other\n\ntype T struct{ F func() }\n\nfunc f() {\n\tvar workflow T\n\tworkflow.F = func() {}\n}\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("never assigned", proc.stdout)

    def test_comment_cannot_clear_unassigned_seam(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            write(root, "internal/cli/workflow/b.go", "package workflow\n\n// TODO: F = nil someday\n")
            write(root, "internal/cli/workflow/c.go", "package workflow\n\nfunc f(cfg *T) { cfg.F = nil }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("never assigned", proc.stdout)

    def test_grouped_spec_counts_each_name(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar (\n\tA, B func()\n)\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["seams"]["internal/cli/workflow"], 2)

    def test_string_content_does_not_confuse_block_state(self):
        with tempfile.TemporaryDirectory() as root:
            body = "package workflow\n\nvar (\n\t// a comment with a ) at column 0 below inside a raw string\n\ts = `)\nvar (\n`\n\tA func()\n)\n"
            write(root, "internal/cli/workflow/a.go", body)
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["seams"]["internal/cli/workflow"], 1)

    def test_assigned_only_in_test_file_passes(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            write(root, "internal/cli/workflow/a_test.go", "package workflow\n\nfunc init() { F = nil }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 0, proc.stdout)

    def test_ignored_name_is_exempt(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n\nvar F func()\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            path = os.path.join(root, ".mivia/policy/seam-baseline.json")
            with open(path) as fh:
                base = json.load(fh)
            base["ignore"] = ["F"]
            with open(path, "w") as fh:
                json.dump(base, fh)
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 0, proc.stdout)


class FanoutTest(unittest.TestCase):
    def test_chat_fanout_increase_fails(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/chat/a.go", "package chat\n\nimport _ \"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger\"\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            write(root, "internal/cli/chat/b.go", "package chat\n\nimport _ \"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery\"\n")
            proc = run_gate(root)
            self.assertEqual(proc.returncode, 1, proc.stdout)

    def test_fanout_counts_real_imports_only(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/chat/a.go", "package chat\n\n// mentions mivia-agent/internal/workflows/ledger in a comment\n\nfunc f() { s := \"mivia-agent/internal/workflows/delivery\"; _ = s }\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["chat_workflows_fanout"]["internal/cli/chat"], 0)

    def test_raw_string_import_block_does_not_count(self):
        with tempfile.TemporaryDirectory() as root:
            body = "package chat\n\nvar usage = `example:\nimport (\n\t\"github.com/MiviaLabs/mivia-agent/internal/workflows/fake\"\n)\n`\n"
            write(root, "internal/cli/chat/a.go", body)
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["chat_workflows_fanout"]["internal/cli/chat"], 0)

    def test_dot_import_counts(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/chat/a.go", "package chat\n\nimport (\n\t. \"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger\"\n)\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["chat_workflows_fanout"]["internal/cli/chat"], 1)

    def test_outside_chat_not_counted(self):
        with tempfile.TemporaryDirectory() as root:
            write(root, "internal/cli/workflow/a.go", "package workflow\n")
            proc = run_gate(root, ["--generate"])
            self.assertEqual(proc.returncode, 0, proc.stderr)
            with open(os.path.join(root, ".mivia/policy/seam-baseline.json")) as fh:
                base = json.load(fh)
            self.assertEqual(base["chat_workflows_fanout"].get("internal/cli/chat", 0), 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
