# Step 0 Plan: embed-agent-instructions
Template-Version: v1

## Goal
Embed the entire ADLC, AGENTS.md, CLAUDE.md, and .ai/ instruction surface into the Go binary so the agent is self-contained when copied to any project.

## Evidence Ledger — Current State

### Current Architecture (before change)
- AGENTS.md, CLAUDE.md, .ai/rules/*, .ai/doctrines/*, .ai/skills/* are **files on disk**
- The binary depends on these files existing in the workspace
- No `//go:embed` usage anywhere in the codebase
- When copied to another project, the agent has NO instructions — it starts blank
- The ADLC and all process rules would be lost

### Target Architecture
- A new `internal/agentkit/` package holds all embedded agent instructions
- `//go:embed` directives embed: AGENTS.md, CLAUDE.md, .ai/*.md, .ai/rules/*.md, .ai/doctrines/*.md, .ai/skills/*/SKILL.md
- The binary serves these embedded files at startup
- When no `.ai/` directory exists in the target workspace, the agent uses embedded defaults
- When `.ai/` DOES exist in the target workspace, it takes precedence (user can override)
- Integration tests verify: binary works without .ai/ dir, embedded content matches source

### Key Design Decisions (to be challenged)
1. **Embedded but overridable**: target project `.ai/` should take precedence over embedded defaults
2. **Server vs filesystem**: does the binary serve embedded files via an in-process HTTP/filesystem, or write them to disk on first run? 
3. **Update model**: if embedded instructions are updated in a new binary, they just replace the old ones — no migration needed
4. **Workspace detection**: the agent checks if `.ai/` exists in CWD. If yes → use it. If no → use embedded.

### Files to Create
- `internal/agentkit/embed.go` — //go:embed directives, EmbedFS accessor
- `internal/agentkit/embed_test.go` — Tests for embedding correctness
- `internal/agentkit/agentkit.go` — Package doc, public API
- `cmd/mivia/main.go` — Wire agentkit into startup (maybe add --write-agent-instructions flag)

### Files to Modify
- `.gitignore` — Add any generated files
- `Makefile` — Add test targets for embed verification

### API Surface (planned)
```go
package agentkit

import "embed"

//go:embed agents.md
var agentsMD embed.FS

//go:embed ai/*
var aiFS embed.FS

// AgentInstructions returns the embedded AGENTS.md content.
func AgentInstructions() string

// Rule returns embedded .ai/rules/<name> content. Returns error if not found.
func Rule(name string) (string, error)

// WriteInstructions writes all embedded instructions to the given directory.
// Used when --init-agent-dir flag is passed.
func WriteInstructions(dir string) error

// HasLocalOverride checks if CWD has a .ai/ directory with user overrides.
func HasLocalOverride() bool

// Resolve returns the effective instruction content: local override first, else embedded.
func Resolve(path string) (string, error)
```

### Test Strategy
| Test | Type | Scenario |
|------|------|----------|
| TestEmbed_AgentsMD | unit | Verify AGENTS.md is embedded and non-empty |
| TestEmbed_ADLCRule | unit | Verify ADLC rule is embedded and contains "Step 0" |
| TestEmbed_AllRules | unit | Verify all .ai/rules/*.md are embedded |
| TestEmbed_AllDoctrines | unit | Verify all .ai/doctrines/*.md are embedded |
| TestEmbed_AllSkills | unit | Verify all .ai/skills/*/SKILL.md are embedded |
| TestWriteInstructions | unit | Write to temp dir, verify files exist and match source |
| TestHasLocalOverride | unit | With/without .ai/ dir, detect correctly |
| TestResolve_LocalFirst | unit | Local .ai/ takes precedence over embedded |
| TestResolve_EmbeddedFallback | unit | Without local .ai/, embedded content returned |
| TestIntegration_NoLocalDir | integration | Run binary without .ai/, verify agent still has instructions |
| TestIntegration_WriteFlag | integration | Run `mivia --init-agent-dir /tmp/x`, verify files written |

### Rollback Criterion
If embedding increases binary size >500KB, the approach needs optimization (compress or trim content).

## Discovery: Files to Embed

The following files from the repo root need to be embedded:
- `AGENTS.md`
- `CLAUDE.md`
- `.ai/INDEX.md`
- `.ai/agent-prompt.md`
- `.ai/invariants.md`
- `.ai/rules/*.md` (10 files)
- `.ai/doctrines/*.md` (2 files)
- `.ai/skills/*/SKILL.md` (multiple)

Total estimated size: <100KB of markdown. Embed overhead is minimal.
