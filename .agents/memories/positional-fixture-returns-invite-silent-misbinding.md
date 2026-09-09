---
id: positional_fixture_returns_invite_silent_misbinding
title: Test fixtures returning several same-typed values get destructured wrong and stay green
content: A fixture that returns multiple positional strings (store, mainDir, wtDir, dbPath) lets callers bind the wrong value to the wrong name; the tests stay self-consistent and green until an unrelated change exposes the swap - return a named struct instead.
importance: medium
tags: [testing, fixtures, go, worktree, review]
updated: 2026-09-08
---

# Positional fixture returns invite silent mis-binding

`worktreeCatalogFixtureNoClose` returns `(store, mainDir, canonicalWt,
dbPath)` - three same-typed strings. Nine callers destructured it as
`store, _, mainDir, _`, binding the WORKTREE directory as the repo root.
Every derived path, principal, and git init stayed self-consistent inside
each test, so all nine were green for their whole life. The swap only
surfaced when a fixture change (seeding the on-disk worktree marker) made
the mis-rooted `git add .` commit the marker into the fixture repo and a
downstream identity check finally disagreed.

**Why it matters:** the compiler cannot catch it, the tests cannot catch
it (they validate their own frame), and review reads `mainDir := ...` as
correct. The bug class costs hours precisely because everything passes.

**What to do instead:** when a fixture returns more than two values of
the same type, return a small named struct (`fixture.MainDir`,
`fixture.WorktreeDir`) so call sites name what they take. When touching an
existing positional fixture, cross-check every caller's destructuring
against the fixture's actual return order before trusting any of the
tests built on it.
