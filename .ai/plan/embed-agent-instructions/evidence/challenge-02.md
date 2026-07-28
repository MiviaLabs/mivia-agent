# Challenge 02: The Invisible Embedding — Architectural Gap Analysis

## Rating: CRITICAL

The plan defines a complete embedding API (`agentkit.Resolve`, `WriteInstructions`, `HasLocalOverride`) but **never closes the loop** between "content is embedded in the binary" and "the agent can discover the content." This is a structural omission, not a missing edge case.

---

## Finding 1: `Resolve` is defined but never called

The plan adds `agentkit.Resolve(dir, relPath) string` — a two-tier lookup: local file first, embedded fallback. But **nobody calls it**. There are exactly three code paths that read instructions at startup, and all bypass `Resolve`:

| Code Path | File | Mechanism | Uses agentkit.Resolve? |
|-----------|------|-----------|------------------------|
| `loadAgentPrompt` | `internal/cli/prompt.go:58` | `os.ReadFile(.ai/agent-prompt.md)` → fallback to string constant | No |
| `attachSessionDispatcher` | `internal/cli/chat_repl.go:76` | `skills.LoadMarkdown(.ai/skills/...)` → `os.ReadDir` + `os.ReadFile` | No |
| Agent tools (`read_file`, `list_dir`, `grep`, `glob`) | `internal/tools/read.go` | `workspace.Root.Resolve` → OS filesystem | No |

**Evidence** — `loadAgentPrompt` in `internal/cli/prompt.go:58-71`:
```go
func loadAgentPrompt(workspaceDir string) string {
    candidate := filepath.Join(workspaceDir, agentPromptPath)
    data, err := os.ReadFile(candidate)  // raw OS read, no agentkit
    if err == nil && len(data) > 0 { ... }
    return defaultAgentPrompt             // compiled-in string constant
}
```

**Evidence** — `attachSessionDispatcher` in `internal/cli/chat_repl.go:76`:
```go
skillReg, err := skills.LoadMarkdown(
    filepath.Join(root, ".ai", "skills"),  // hardcoded OS path
    ...
)
```

**Evidence** — `skills.LoadMarkdown` in `internal/skills/loader.go:19`:
```go
entries, err := os.ReadDir(root)    // raw OS read
data, err := os.ReadFile(path)       // raw OS read
```

**Evidence** — `readFileTool.Execute` in `internal/tools/read.go:80`:
```go
abs, err := t.ws.Resolve(in.Path)   // workspace resolves to OS filesystem
```

**Impact**: Even after the full plan is implemented, an agent starting in a repo without `.ai/` will:
1. Call `read_file(".ai/agent-prompt.md")` → file not found on disk → error or empty.
2. Call `list_dir(".ai/")` → directory not found on disk → empty list.
3. Never discover the content that is safely embedded in the binary.

---

## Finding 2: Auto-write is not wired into startup

The plan defines `WriteInstructions(dir)` and `HasLocalOverride(dir)` in `agentkit`, and adds a `--init-agent-dir` CLI flag in Wave 4. But:

- **`--init-agent-dir` is opt-in** — a user must know the flag exists and run it manually. New users cloning a repo will not do this.
- **No automatic check** — there is no code in `configureChatWorkspace` (`internal/cli/chat_repl.go:56`) or `runChat` that calls:
  ```go
  if !agentkit.HasLocalOverride(workspacePath) {
      agentkit.WriteInstructions(workspacePath)
  }
  ```
  The existing `ensureAgentPromptFile` call at line 117 writes a _single_ file (`.ai/agent-prompt.md`) from a compiled-in string constant. This is the natural injection point, but the plan doesn't replace or extend it to use `agentkit.WriteInstructions`.

**Impact**: Without auto-write on startup, the embedded files are invisible. The binary carries the instructions, but the agent cannot see them.

---

## Finding 3: The `loadAgentPrompt` integration is ambiguous

The plan says the agent will discover instructions "normally" via tools. But `loadAgentPrompt` already has its own compiled-in fallback (`defaultAgentPrompt`). After the plan, there would be **two copies** of the agent prompt:

1. `defaultAgentPrompt` — the compiled-in Go string constant in `internal/cli/prompt.go`.
2. `agentkit.AgentInstructions()` — the embedded `AGENTS.md` from `//go:embed`.

These could diverge. The plan doesn't specify which one wins, or whether `loadAgentPrompt` should be rewritten to call `agentkit.AgentInstructions()` as its fallback instead of the string constant.

**Impact**: Silent drift between `prompt.go`'s `defaultAgentPrompt` and the embedded `AGENTS.md`. Both are compiled into the binary but may differ in content.

---

## Finding 4: The test strategy does not verify end-to-end discovery

The plan has 12 test scenarios (unit + integration), but none test the critical path:

| Test | What it covers | What it misses |
|------|---------------|----------------|
| `TestIntegration_NoLocalDir` | Call `AgentInstructions()` Go API | Does **not** verify that `read_file(".ai/agent-prompt.md")` succeeds |
| `TestWriteInstructions` | Files written to temp dir | Does **not** verify that tools can read them after write |
| `TestResolve_LocalFirst` | `agentkit.Resolve` Go API | Does **not** verify that `loadAgentPrompt` uses `Resolve` (it doesn't) |

The integration test runs a "test binary without .ai/" but only checks `agentkit.AgentInstructions()` — a direct Go call that bypasses the tool chain entirely.

**Impact**: All tests could pass green while the agent in a real session sees zero instructions.

---

## Required Architectural Changes

1. **Auto-write on startup**: In `configureChatWorkspace` (or `runChat`), add:
   ```go
   if !agentkit.HasLocalOverride(workspacePath) {
       if err := agentkit.WriteInstructions(workspacePath); err != nil {
           // log warning
       }
   }
   ```
   This is the bridge between "embedded in binary" and "visible to agent tools."

2. **Replace `ensureAgentPromptFile`**: The existing function writes only `agent-prompt.md`. Replace its call with `agentkit.WriteInstructions` which writes all `.ai/` files from the embedded FS.

3. **Rewrite `loadAgentPrompt`** to use `agentkit.AgentInstructions()` as its fallback instead of the `defaultAgentPrompt` string constant, eliminating the dual-source drift.

4. **Rewrite `skills.LoadMarkdown` call** to also resolve from embedded content when no local skills directory exists, or ensure `WriteInstructions` writes skills too.

5. **Add an integration test** that starts a session in a temp directory without `.ai/`, then verifies `read_file(".ai/agent-prompt.md")` returns non-empty content via the real tool execution path (not just the Go API).

---

## Summary

| # | Issue | Severity | Status |
|---|-------|----------|--------|
| 1 | `Resolve` is defined but unreachable by any agent code path | Critical | Unaddressed |
| 2 | No auto-write on startup; `--init-agent-dir` is opt-in manual flag | Critical | Unaddressed |
| 3 | Dual-source drift: `defaultAgentPrompt` vs embedded `AGENTS.md` | Medium | Unaddressed |
| 4 | Tests bypass the tool chain; end-to-end discovery untested | High | Unaddressed |
