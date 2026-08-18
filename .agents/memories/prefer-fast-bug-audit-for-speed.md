---
id: prefer_fast_bug_audit_for_speed
title: Fast bug hunt requests → bug-fix-fast / fast-bug-audit, not the slow default
content: When the user wants a quick/fast bug hunt in this repo, dispatch bug-fix-fast.toml or the fast-bug-audit skill directly - not the default bug-fix.toml / bug-audit path, which is deliberately slow and exhaustive.
importance: medium
tags: workflows, skills, bug-hunting
---

When the user asks for a fast, quick, or opportunistic bug hunt, default to
the fast path, not the habitual slow one:

- Workflow dispatch: `mivia workflow run bug-fix-fast`
  (`.mivia/workflows/bug-fix-fast.toml`), not `bug-fix` - same shape and
  delivery, only the hunt step's skill/template differ (`fast-bug-audit` /
  `templates/bugfix-hunt-fast.md` vs `bug-audit` / `templates/bugfix-hunt.md`).
- Interactive/direct skill invocation: `fast-bug-audit`
  (`.mivia/skills/fast-bug-audit/SKILL.md`), not `bug-audit`.
- Feature-delivery task text: the template at
  `.mivia/templates/bug-hunt-task-fast.md` (scope placeholder at the end,
  fill in and paste as the `task` input).

**Why:** `bug-audit` (and the plain `bug-fix` workflow) is deliberately slow
and exhaustive - invariant-first derivation, mandatory same-class sweep per
finding. `fast-bug-audit` trades that exhaustiveness for speed: jump between
candidates freely, forced confirmed/dropped/needs-one-check triage per
candidate, sweep optional. The user asked for this after finding the default
`bug-audit` too slow, then separately worried about the agent drifting back
to the slow path out of habit in a future session - this memory exists to
prevent that. Skill selection is NOT a runtime choice by the model; it is
fixed per workflow TOML at compile time
(`internal/workflows/definition/types.go:33`, a static string field, not
templated), so picking the right *workflow file* at dispatch time is what
actually matters, not phrasing in the task text.

**How to apply:** any time the user's phrasing signals urgency/speed/"don't
take too long" for a bug hunt, reach for the fast variant first and only
fall back to the slow, exhaustive `bug-audit` path when thoroughness is
explicitly wanted over speed.
