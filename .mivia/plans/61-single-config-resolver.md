# Plan 61 - One Resolver Owns Config Defaults

Status: Design-ready - Step 0 challenge not started

Class: `DC-5` in `.mivia/quality/defect-taxonomy.md`.

## Goal

Make a configured value reach the runtime unchanged, and make a misplaced or
misspelled key fail loudly. Remove the defect class instead of adding one more
probe for it.

## Why this is a plan and not a fix

The class has two mechanisms, and both are still live at HEAD.

**Mechanism 1 - the zero sentinel.** A numeric bound uses `0` for "no limit". A
guard of the form `len(x) >= max` then reads `0` as "already at the limit" and
returns nothing. The repository already knows this: commits `9475bee`,
`b8e2a20`, and `ac01ccc` closed it across the tool defaults, and
`ac01ccc` added integration tests for a zero-means-unlimited contract.

**Mechanism 2 - layered replacement.** More than one layer replaces a zero
value with a hardcoded default, so the caller's value cannot reach the runtime.
One reported instance had four layers: `max_steps=0` written at TOML top level
instead of under `[chat]` and silently dropped, `resolveSubagentConfig`
replacing every zero, `subagents.New` replacing zero bounds again, and
`setupAgentLoop` replacing `MaxSteps<=0` a third time.

The fixes so far were per-field. The codebase is now mid-migration, which is
the worst state to stay in:

- 11 fields in `internal/config/types.go` use a pointer type (`*int`,
  `*float64`, `*bool`), where `nil` means unconfigured and `0` means zero.
  These are correct.
- 9 fields in `internal/config/load.go` still fill from a zero sentinel
  (`if cfg.X == 0` or `<= 0`): `InlineOutputBytes`, `SchemaRetryMax`,
  `MaxBodyBytes`, `MaxMessagesPerTask`, `MailboxCapacity`,
  `MaxPendingQuestions`, `MaxAsksPerTask`, `MaxReferralDepth`,
  `MaxReferralSpawnsPerRun`. For these, a caller cannot express zero.
- `internal/config/load.go:286` decodes `mivia.toml` without
  `DisallowUnknownFields()`. A misplaced or misspelled key is dropped in
  silence. `internal/config/agents_parse.go:24` already rejects unknown keys on
  agent files, so the product contains both behaviours.

Two mechanisms, two code shapes in one struct, and no gate that stops the next
field from picking the wrong one. A probe catches the next instance. Only a
single owner removes the class.

## Design

### D1 - One representation for "unconfigured"

Every optional scalar uses a pointer type. `nil` means the caller said nothing;
a value means the caller said that value, including `0`. Delete the zero
sentinel as a signalling device.

### D2 - One resolver, one call site

`internal/config` owns defaults. A resolver runs once, at load, and returns a
resolved struct in which every optional scalar is set. No layer below config
may substitute a default. Downstream code reads the resolved value.

Make the seam enforceable rather than a convention: the resolved type is
distinct from the parsed type, so a consumer cannot receive an unresolved
struct. The parsed type carries pointers; the resolved type carries plain
values. A consumer that holds the resolved type has no `nil` to test and so has
nothing to substitute.

### D3 - The resolved value is what the runtime reads

Write the resolved value back into the struct the rest of the program reads.
`18804cf` fixed exactly this for the SQLite store path: the resolver computed a
workspace-hashed path and downstream code read the unresolved field.

### D4 - A misplaced key fails at load

Add `DisallowUnknownFields()` to the `mivia.toml` decoder, matching the agent
file loader. This is a behaviour change for existing configs that carry a
misplaced key, so it needs the Step 0 decision in §Open decisions.

### D5 - A gate, so the class cannot return

A contract test asserts that every optional scalar in the parsed config type is
pointer-typed, and that no package below `internal/config` fills a config field
from a zero test. Without this the migration decays: 11 fields are already
correct and 9 are not, which is what an ungated convention produces.

## Open decisions (Step 0)

1. **Unknown-key strictness.** Reject at load, or warn once and continue? A
   reject is honest and matches the agent loader. It also fails startup for a
   config that works today. A warn keeps the silent-drop defect in a quieter
   form. Recommendation: reject, with the offending key and the expected table
   named in the error, because the defect this plan closes is caused by silence.
2. **Scope of the pointer migration.** All optional scalars, or only bounds and
   budgets? A partial migration recreates the mid-migration state this plan
   exists to end. Recommendation: all optional scalars in the parsed type.
3. **Distinct resolved type.** A separate resolved type is enforceable but
   touches every consumer. The cheaper option keeps one type and relies on the
   contract test from D5. Decide by counting consumers at Step 0.
4. **Fields where zero is meaningless.** `MailboxCapacity = 0` may have no
   valid meaning. State per field whether zero is legal, unlimited, or
   rejected. A field that rejects zero must say so in an error, not silently
   substitute.

## Files

- `internal/config/types.go` - pointer types for optional scalars; resolved type
- `internal/config/load.go` - single resolver; `DisallowUnknownFields`; delete
  the 9 zero-sentinel fills
- `internal/subagents/subagents.go` - stop replacing zero bounds
- `internal/cli/*` agent loop setup - stop replacing `MaxSteps`
- `.mivia/mivia.toml.example` - document what zero means per field
- contract test for D5

## Test plan

1. Per field: `nil` resolves to the documented default.
2. Per field: an explicit `0` survives to the runtime, or is rejected with a
   named error. Never silently replaced.
3. A key at the wrong table level fails at load, and the error names the key and
   the expected table.
4. An unknown key fails at load.
5. No layer below config changes a resolved value: assert the value the runtime
   reads equals the value the resolver produced (the `18804cf` regression).
6. D5 contract test: a new non-pointer optional scalar fails the gate.
7. Migration safety: an existing valid config still loads unchanged.

## Readiness scorecard

| Gate | State |
|------|-------|
| Defect class confirmed at HEAD | Yes - 9 zero-sentinel fills, no `DisallowUnknownFields` on `mivia.toml` |
| Evidence cited | Yes - `9475bee`, `b8e2a20`, `ac01ccc`, `18804cf`, `load.go:286`, `agents_parse.go:24` |
| Open decisions recorded | Yes - 4 |
| Step 0 challenge | Not started |
| `architecture-review` at Step 0 | Required before the plan locks |
