---
id: rg_subprocess_reads_stdin
title: ripgrep with no path argument reads stdin, not the tree
content: A subprocess rg call with no search path searches stdin whenever stdin is not a tty.
importance: high
tags: [[scripts, grep, subprocess]]
---

# ripgrep with no path argument reads stdin, not the tree

A `subprocess.run(["rg", ...])` call with **no search path** searches **stdin**
whenever stdin is not a tty. Every non-interactive context has a non-tty stdin:
CI, a git hook, an agent session.

Two failure modes, both silent:

- stdin never reaches EOF → the call **blocks forever**. Observed: a
  `make validate-invariants` left running 22 minutes, the parent parked in
  `poll` waiting on the child.
- stdin is `/dev/null` → **zero matches**, which a gate reads as "nothing to
  report" and passes.

Measured on this repo: `rg -n "^func Test" -g "*_test.go"` returns **10634**
matches with an explicit path and **0** with a non-EOF stdin.

This shipped twice, copy-pasted, in `scripts/validate_invariants.py` and
`scripts/invariant_coverage.py` - both on the `make verify` chain, so
`make verify` itself could wedge. The second site had grown a fallback blaming
"Homebrew's ripgrep on macOS" for the empty result: a misdiagnosed mechanism
that had already been rationalised into a comment.

**Always pass an explicit path AND `stdin=subprocess.DEVNULL`.** Enforced by
`scripts/check_subprocess_stdin.py` (`make subprocess-stdin-check`, also in the
pre-commit staged map for `scripts/*.py`); policy in
`.mivia/policy/subprocess-stdin.json`.

If a gate ever appears to hang, check `/proc/<pid>/wchan` and
`ls -l /proc/<pid>/fd/0` before assuming it is slow.
