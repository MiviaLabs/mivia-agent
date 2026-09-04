---
id: test_fixtures_use_guaranteed_binaries_only
title: Test fixtures use only platform-guaranteed binaries
content: A test fixture that shells out may invoke only binaries every CI surface guarantees.
importance: high
tags: [tests, ci, fixtures]
updated: 2026-09-04
---

# Test fixtures must use only platform-guaranteed binaries

A test fixture that shells out may only invoke binaries every CI surface
guarantees: coreutils that are real files under /usr/bin (cat, printf via
the shell), git, and go. Do not use awk, and do not assume a POSIX shell
exists on Windows.

**Why:** The stop-hook bound fixture (fixed in 0e819448) used awk and broke
two CI surfaces at once. The verifier sandbox (bwrap) binds /usr/bin but
not /etc, and Ubuntu's awk is a symlink through /etc/alternatives, so awk
resolves to nothing inside the sandbox. Windows has no awk and no sh; the
test's batch translator also keyed on the old fixture text, so the change
silently fell through to a body Windows cannot run.

**How to apply:** Generate bulk fixture data in Go (os.WriteFile) and have
the hook script only read it (cat on POSIX, type on Windows). When a test
translates a POSIX fixture body for Windows, key the translator on the new
body in the same commit - grep the translator for the old text. The
sandbox gate (TestSandboxRunsRepositoryProfile) is what catches this
class; run it via the verifier-integration job, not locally. Related:
[[sibling-implementations-drift]].
