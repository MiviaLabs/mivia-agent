# Agent Self-Contained System Prompt

The default system prompt for the **agent mode** (tools enabled) lives in:

**`internal/cli/chat.go`** → `defaultAgentSystemPrompt`

It is compiled into the binary. When you rebuild and relaunch `mivia chat`, the agent automatically knows the full project state without needing conversation history.

## What the prompt contains

### Project identity
- Module path, binary name, what the project is
- That it's **self-improving** — the agent builds the agent tooling

### Commit history (chronological)
All 5 commits on master with their purposes, so the agent knows what's been done.

### Full architecture
Every Go package, its file, and its purpose:
- `internal/provider/` — LLM adapters, retry middleware, context management
- `internal/agent/` — tool-calling loop
- `internal/chat/` — multi-turn session state
- `internal/cli/` — REPL, commands, UI
- `internal/tools/` — workspace operations (read, write, search, run)
- `internal/workspace/` — path confinement
- `internal/config/` — TOML loading

### Verified deliverables
What works and is tested:
- ✅ Retry middleware (20 tests)
- ✅ Context window management (11 tests)
- ✅ CLI UI improvements

### Next priorities (in order)
1. Session persistence (save/load conversations)
2. Parallel tool execution
3. Streaming + tools together
4. `ctx.Done()` checks in write/search tools
5. Configurable context budget in TOML

### How to test and build
- `go test ./...` / `go build -o mivia ./cmd/mivia`
- `make verify` for full gates

### Commit conventions
Allowed types, scopes, format rules.

### Non-negotiables
The standard agent rules: prefer tools, no secrets, verify claims, etc.

## Updating

When you implement a new feature or change the architecture, update `defaultAgentSystemPrompt` in `internal/cli/chat.go` so the next rebuild inherits the knowledge.
