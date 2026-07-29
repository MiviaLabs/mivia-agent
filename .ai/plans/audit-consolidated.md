# Consolidated Bug Audit — Allowlist Refactor (Phases 1-3 Validated)

All findings validated against actual source code on 2025-07-29.

---

## P0 — Behavioral Regressions (Fix Before Continuing)

| ID | Bug | Files | Priority |
|----|-----|-------|----------|
| B1 | `GIT_*` and `NODE_*` prefixes missing from `DefaultEnvAllowlistPrefixes` — previously-allowed env vars now blocked | `internal/tools/run.go:296-299` vs `run.go:378-385` | **P0** |
| B2 | `DisableTools` case-sensitive — mixed-case TOML values silently ignored | `internal/tools/tools.go:436-445` | **P0** |

## P1 — Correctness Bugs

| ID | Bug | Files | Priority |
|----|-----|-------|----------|
| B3 | `CCX` bogus env var (typo) | `internal/tools/run.go:285` | **P1** |
| B4 | `SecretPathExceptions` appends to global — test pollution | `internal/tools/tools.go:412` | **P1** |
| B5 | Package-level `filterEnv()` bypasses user config (latent) | `internal/tools/run.go:262-263` | **P1** |
| B6 | `resolveToolsConfig` doesn't propagate `RedactToolArgs` default (latent) | `internal/config/load.go:206-221` | **P1** |
| B7 | `resolveToolsConfig` ignores all 9 slice-based fields (no validation) | `internal/config/load.go:206-221` | **P1** |

## P2 — Design Clarity

| ID | Bug | Files | Priority |
|----|-----|-------|----------|
| B8 | `RedactToolArgs` has two independent sources with OR logic | `internal/config/types.go:42,66`, `tools/tools.go:450`, `cli/chat_repl.go:40,128` | **P2** |
| B9 | Env blocklist only matches exact names, not prefixes (undocumented) | `internal/tools/run.go:316-349` | **P2** |
