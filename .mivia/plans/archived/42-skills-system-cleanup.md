# 42 - Skills system cleanup

**Status:** Implemented. See `refactor(ai): remove dead code and collapse duplication in skills system`.
**Date:** 2025-07-14
**Depends on:** nothing.
**Blocks:** `05` P2 (Definition.Tools), `06` (agent-skill binding).
**Blast radius:** LOW (internal refactoring, no behavioral change).

---

## 1. Purpose

Remove dead code, collapse duplicated parse paths, and consolidate shared
resource-tool injection in the skills system. No behavioral change; all
findings are confirmed by adversarial sub-agent challenges that traced every
production call site.

### Confirmed-by-challenge results

| # | Finding | Challenge verdict | Confidence |
|---|---------|------------------|------------|
| AR-1 | `Definition.Run` / `skillRunner` dead on production path | **CONFIRMED** - every traced path uses MultiStepHandler as Subagent; `RegisterAll` registrations are inert entries nothing dispatches to; `Registry.Invoke` and `RegisterAllAsSubagents` are test-only | High |
| AR-2 | `Definition.Tools` never populated, `Select` guard dead | **CONFIRMED** - `knownSkillKeys` excludes `"tools"`, `Registry.Invoke` (sole caller of `Select`) has zero production callers; field is reserved for plan 06 agent-skill binding | High |
| AR-3 | Dual resource-tool injection duplication | **CONFIRMED** - clone→conflict-check→create→register sequence is character-identical; both use `*tools.Registry` and `*skills.SkillActivation`; nil-guard is upstream of shared logic and stays in callers | High |
| AR-4 | `LoadMarkdown` lacks filtering, test-only | **CONFIRMED** - all 18 callers are in `*_test.go`; production uses `LoadMarkdownSources`; exported unnecessarily | High |
| AR-5 | Double `ParseFrontmatterKnown` call | **CONFIRMED** - exact same data normalized and parsed twice; 2/4 normalize, 2/3 split, 2/3 closing-scan, 1/2 key-value parse all redundant | High |
| AR-6 | Split key extraction + redundant closing delimiter | **CONFIRMED** - `parseMarkdown` and `parseSkillMarkdown` extract different keys from same parsed result; `parseMarkdown` re-scans for closing `---` already found by `frontmatterLines` | High |
| Rejected-2 | NTFS hardlink gap | **PARTIAL** - Windows-only; symlink/SameFile/os.Root mitigations remain; no Go stdlib API for NTFS link counts; code documents the limitation | Medium |

---

## 2. Changes

### Wave 1 - Dead-code removal (AR-1, AR-2, AR-4)

These are independent removals. No behavioral change; all dead paths have zero
production callers.

#### 2.1 Remove `Definition.Run`, `skillRunner`, `handler{d}`, `RegisterAllAsSubagents`, `Registry.Invoke` (AR-1)

| File | Change |
|------|--------|
| `internal/skills/skills.go` | Remove `Run` field from `Definition`; remove `handler` struct, `handler.Invoke`, `Registry.Invoke`; remove `RegisterAllAsSubagents`; remove `validate` / `validateValue` (only used by `handler.Invoke`) |
| `internal/skills/loader.go` | Remove `skillRunner` function; remove line `def.Run = skillRunner(...)` from `loadSkillDirAt`; remove `completer` and `model` parameters from `LoadMarkdown` and `LoadMarkdownSources` and all callers |
| `internal/cli/dispatcher.go` | Remove `skillReg.RegisterAll(d)` call (line ~182); the `runtime.Skill` registrations were inert |
| `internal/cli/model_binding.go` | Update `loadSessionSkills` call (remove completer/model args) |
| `internal/skills/skills_test.go` | Rewrite tests to use `Register` + dispatcher directly instead of `RegisterAllAsSubagents` / `Registry.Invoke` |
| `internal/cli/provider_model_test.go` | Remove `definition.Run(...)` call; stub differently |
| `internal/cli/slash_catalog_test.go` | Remove `definition.Run = func(...)` stub |
| `internal/cli/delegation_test.go` | Update `skills.LoadMarkdown` call signature |

**Dependency simplification:** removing `skillRunner` eliminates the `skills →
provider` import. The `skills` package will depend only on `runtime`.

#### 2.2 Document `Definition.Tools` as reserved (AR-2)

Do NOT remove the field or the `Select` guard - plan 06 explicitly
depends on populating `Definition.Tools` from frontmatter. Instead:

| File | Change |
|------|--------|
| `internal/skills/skills.go` | Add doc comment on `Tools` field: `"Tools is reserved for plan 06 agent-skill binding. It is not populated by plan 05's agent model. The Select guard is intentionally retained for that milestone."` |
| `internal/skills/skills.go` | Add doc comment on `Select` method: `"Select validates version and tool availability. The tool-availability guard is vacuous until Definition.Tools is populated (plan 06)."` |

#### 2.3 Unexport `LoadMarkdown` (AR-4)

| File | Change |
|------|--------|
| `internal/skills/loader.go` | Rename `LoadMarkdown` → `loadMarkdown`; make `loadMarkdownSource` its only implementation (delegate to `LoadMarkdownSources` with single-source slice) |
| `internal/cli/delegation_test.go` | Replace `skills.LoadMarkdown(root, comp, model)` with `skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, comp, model, skills.LoadOptions{})` |
| `internal/cli/prompt_test.go` | Same migration |
| `internal/cli/skill_activation_handler_test.go` | Same migration (3 call sites) |
| `internal/skills/loader_test.go` | Update all intra-package calls to `loadMarkdown` |

---

### Wave 2 - Duplication collapse (AR-3, AR-5, AR-6)

Independent of Wave 1; can ship in parallel.

#### 2.4 Extract `injectSkillResourceTool` helper (AR-3)

New file `internal/cli/skill_resource_tool.go`:

```go
// injectSkillResourceTool clones the given registry, checks for an existing
// skill resource tool (returning an error on conflict), registers a new
// scoped reader bound to the activation, and returns the augmented clone.
// The caller is responsible for calling activation.Close() when done.
func injectSkillResourceTool(
    registry *tools.Registry,
    activation *skills.SkillActivation,
) (*tools.Registry, error) {
    clone := registry.Clone()
    if _, exists := clone.Get(tools.SkillResourceToolName); exists {
        return nil, fmt.Errorf("skill resource capability conflict")
    }
    clone.Register(tools.NewSkillResourceTool(
        func(ctx context.Context, id string) (string, string, error) {
            content, err := activation.Read(ctx, id)
            if err != nil {
                return "", "", err
            }
            return content.Text, "skill resource loaded: "+content.ID, nil
        },
        activation.ToolKey(),
        activation.ToolResultBudget(),
    ))
    return clone, nil
}
```

| File | Change |
|------|--------|
| `internal/cli/skill_resource_tool.go` | **New file** - `injectSkillResourceTool` function |
| `internal/cli/skill_activation_handler.go` | Replace lines 27-41 with `injectSkillResourceTool(h.template.FullRegistry, activation)` |
| `internal/cli/tui_start.go` | Replace `prepareSkillTurn` lines 67-83 with nil-guard + `injectSkillResourceTool(m.session.Tools, activation)` |

#### 2.5 Collapse double frontmatter parse (AR-5, AR-6)

| File | Change |
|------|--------|
| `internal/skills/frontmatter.go` | Add `ParseFrontmatterKnownWithClosing(data []byte, known map[string]bool) (map[string]any, int, error)` that returns the closing delimiter line index alongside the parsed map |
| `internal/skills/loader.go` | Remove `parseMarkdown` function entirely. Rewrite `parseSkillMarkdown` to call `ParseFrontmatterKnownWithClosing` once, extract all six keys (`name`, `description`, `triggers`, `argument-hint`, `short-description`, `user-invocable`) from the single result, and derive `instructions` from the closing index |

Collapsed `parseSkillMarkdown` sketch:

```go
func parseSkillMarkdown(data []byte) (parsedSkill, error) {
    normalized := normalizeNewlines(string(data))
    m, closing, err := ParseFrontmatterKnownWithClosing([]byte(normalized), knownSkillKeys)
    if err != nil {
        return parsedSkill{}, err
    }
    var instructions string
    if m == nil {
        instructions = strings.TrimSpace(normalized)
    } else {
        lines := strings.Split(normalized, "\n")
        instructions = strings.TrimSpace(strings.Join(lines[closing+1:], "\n"))
    }
    parsed := parsedSkill{userInvocable: true}
    if m != nil {
        parsed.name, _ = m["name"].(string)
        parsed.description, _ = m["description"].(string)
        switch tv := m["triggers"].(type) {
        case []string:
            parsed.triggers = tv
        case string:
            if tv != "" {
                parsed.triggers = []string{tv}
            }
        }
        parsed.argsHint, _ = m["argument-hint"].(string)
        parsed.shortDescription, _ = m["short-description"].(string)
        if v, ok := m["user-invocable"].(string); ok && v != "" {
            switch strings.ToLower(strings.TrimSpace(v)) {
            case "true":
                parsed.userInvocable = true
            case "false":
                parsed.userInvocable = false
            default:
                return parsedSkill{}, fmt.Errorf("user-invocable must be true or false")
            }
        }
    }
    parsed.instructions = instructions
    return parsed, nil
}
```

---

## 3. NOT in scope

| Item | Reason |
|------|--------|
| Remove `RegisterAll` | Retained for coordinator/skill-tool use; removing is a separate decision |
| Populate `Definition.Tools` from frontmatter | Depends on plan 06 agent-skill binding |
| Agent-skill binding enforcement | Depends on plan 06 |
| NTFS hardlink detection | Platform hardening; no Go stdlib API; track as residual |
| Switch `hasSingleLink` on non-Unix to `false` | Would break resource loading on all non-Unix; documented residual is correct |
| Replace custom frontmatter parser with gopkg.in/yaml.v3 | Standard YAML cannot provide fail-closed rejection of block scalars, anchors, and nested maps |

---

## 4. Verification

```bash
go test ./internal/skills/... ./internal/tools/... ./internal/cli/... ./internal/runtime/... -race
make verify && make invariants
```

### Per-wave tests

**Wave 1:**

| Test | What it asserts |
|------|-----------------|
| `TestSkillRegistryAfterDeadCodeRemoval` | `Register` works; `Definition` has no `Run` field; `skills` package has no `provider` import |
| `TestLoadMarkdownUnexported` | `loadMarkdown` is unexported; cross-package tests use `LoadMarkdownSources` |
| `TestSelectToolsGuardDocumented` | `Definition.Tools` field has doc comment; `Select` guard compiles and is documented as vacuous |

**Wave 2:**

| Test | What it asserts |
|------|-----------------|
| `TestInjectSkillResourceTool` | Conflict check rejects existing tool; successful injection returns clone with tool registered |
| `TestSingleParseSkillMarkdown` | All six known keys extracted correctly; instructions body correct; no double-parse side effects |
| `TestParseFrontmatterKnownWithClosing` | Returns correct closing index for multi-line frontmatter; returns -1 for no-frontmatter documents |

### Mutation proofs

| # | Mutation | Test that MUST fail |
|---|----------|---------------------|
| M1 | Re-add `Run` field to `Definition` | Compilation fails (no callers, no assignment) or `TestSkillRegistryAfterDeadCodeRemoval` |
| M2 | Use `LoadMarkdown` from cross-package test | Compilation fails (unexported) |
| M3 | Hardcode `injectSkillResourceTool` to skip conflict check | `TestInjectSkillResourceTool` - conflict returns nil error |
| M4 | Call `ParseFrontmatterKnown` twice in `parseSkillMarkdown` | `TestSingleParseSkillMarkdown` - detects double-parse via side-effecting mock or coverage diff |
| M5 | Remove `Definition.Tools` field | `TestSelectToolsGuardDocumented` - field missing |

---

## 5. Rollback criterion

If any wave breaks the test suite in a way that cannot be fixed within the
wave's blast radius, revert the wave independently. Waves are independent.

If removing `RegisterAll` (line ~182 in `dispatcher.go`) causes a coordinator
or skill-tool path to fail, revert only that line - the rest of Wave 1 is
unaffected.

If `ParseFrontmatterKnownWithClosing` changes `ParseFrontmatterKnown` behavior
for any existing caller, revert Wave 2 and add the closing-index return as a
new exported function instead of modifying the existing one.

---

## 6. Ordering and parallelism

```
Wave 1 (AR-1 + AR-2 + AR-4)  ──┐
                               ├── merge, verify, ship
Wave 2 (AR-3 + AR-5 + AR-6)  ──┘
```

Waves are independent. Ship in either order. AR-1 is the highest-value change
(eliminates `skills → provider` dependency, removes ~80 lines of dead code).
