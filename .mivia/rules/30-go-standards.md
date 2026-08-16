# Go Standards

Product binary: **`mivia`**. Module layout follows standard Go practice for MiviaLabs CLIs.

## Layout

- Command entrypoint: `cmd/mivia/` only. No `cmd/mivia-agent/`.
- Reusable implementation: `internal/` with small, purpose-named packages.
- Do not create public library packages until a task requires an external API.
- Tests live next to code as `*_test.go`; integration tests may use `test/` or build tags when justified.
- Generated or embedded assets stay package-local; runtime artifacts go under `.mivia/runs/` (gitignored), never under `internal/`.

## Size Limits (anti-spaghetti)

Mechanical policy: `.mivia/policy/go-structure.json`, enforced by `scripts/check_go_structure.py` on pre-commit (staged) and pre-push / `make structure-check` (full tree).

| Limit | Soft (warn) | Hard (fail) |
|-------|-------------|-------------|
| Production `.go` file LOC | 500 | 800 |
| `*_test.go` file LOC | 800 | 1200 |
| Function body LOC | 80 | 120 |
| Staged file bytes (any type except binary DBs: *.db, *.sqlite, *.sqlite3) | - | 500 KiB (`file-size-check`) |

**Agent rules:**

- Prefer **new focused files** over growing a 500+ line file.
- Prefer **extracting helpers** when a function approaches 80 lines; do not land new functions over 120 lines.
- **Do not raise** baseline `maxLines` in `go-structure.json` to silence the gate. Only lower it after splits.
- Files listed under `baseline.files` are grandfathered debt: they may not grow past `maxLines`; prefer splitting them when touching that area.
- `write_file` is also capped at 500 KiB content. Do not use `run_command` / `search_replace` to bypass size policy for huge blobs.

## Errors

- Return errors from library code; do not `panic` for expected failures.
- Wrap with `%w` when callers need to unwrap.
- Use `errors.Is` for sentinels and `errors.As` for typed errors.
- Error strings are lowercase sentence fragments without trailing punctuation unless a proper noun requires capitalization.
- User-facing CLI errors may be capitalized sentences; keep them free of secrets and absolute home paths when avoidable. Do not rely on runtime redaction to clean an error message: it is off unless the workspace configures it (rule 10).

## Naming

- Package names: lowercase, short, single-word.
- Initialisms in exported names: `URL`, `HTTP`, `ID`, `API`, `JSON`, `YAML`, `MCP`.
- File names: lowercase; underscores only when needed for clarity.
- Binary and install names: `mivia`. Do not introduce alternate binary names in build scripts without an explicit product decision.

## Comments And Headers

- Package comments start with `Package <name>`.
- Every exported identifier has a doc comment starting with the identifier name.
- Comments explain contracts, edge cases, and invariants; they do not narrate obvious assignments.
- Prefer linking to canonical docs under `docs/` for long product rationale rather than duplicating essays in comments.
- **Do not reference plan waves, session numbers, round numbers, or audit-finding labels in code comments** (e.g. "Wave 8 audit finding #1", "round 2 fix", "this session's change"). A future session or reader has no access to that plan and the label carries no information once the plan is closed. Describe the actual invariant, race, or contract instead: what would break, under what interleaving or input, and why the code guards against it. Plan/wave history belongs in the commit message (`Regression:`/`Class:`/`Sweep:` trailers) and the plan file itself, not in the source.
- **A comment block past 25 lines is a signal to move the rationale to `docs/` and link it**, not a hard error — mechanically enforced as informational only via `scripts/check_go_structure.py` (`commentBlockLines`, `.mivia/policy/go-structure.json`); dense multi-paragraph WHY comments below that are established style here, not debt. Past 30 lines it hard-fails the same gates as file/function LOC (pre-commit, pre-push, `make verify`), growth-only-grandfathered for the handful of files already past it when the check landed.

## Embedding

- Use `//go:embed` only for static templates or fixtures required by the binary.
- Embed patterns are relative to the package directory and must not include `.git`, symlinks, parent traversals, or generated runtime artifacts.
- Add tests that fail when an embedded template is missing or malformed.

## Concurrency

- Prefer `context.Context` for cancellation and deadlines on all blocking/outbound work.
- No unbounded goroutine spawns; every fan-out has a documented cap (see `.mivia/rules/50-concurrency-subagents.md`).
- Document lock ordering where multiple mutexes are held.
- Race-prone packages must be testable with `-race` in CI.

## Dependencies

- Avoid new third-party dependencies unless justified by risk, maintenance, and security review.
- Prefer stdlib when equivalent.
- Pin modules via `go.mod` / `go.sum`; no floating pseudo-versions in release tags without review.

## CLI Conventions

- Cobra (or the repo’s chosen CLI framework) roots at `cmd/mivia`.
- Flags have tests for defaults, validation, and help text stability where contracts are user-facing.
- Exit codes: `0` success; non-zero for user/input errors vs internal failures must be consistent and documented in `docs/product/agent.md`.
