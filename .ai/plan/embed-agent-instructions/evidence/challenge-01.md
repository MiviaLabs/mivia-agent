# Challenge 01: Design & Implementation Gaps

## Finding 1: `mkdir` system command is broken (HIGH)
- **What's wrong**: `run_command(["mkdir", ...])` fails with a system error on every invocation, even for simple relative paths.
- **Impact**: ADLC instructs agents to use `mkdir -p` for creating plan directories. It doesn't work. Agents can't create audit/evidence subdirs.
- **Fix**: Use `write_file` which auto-creates parent directories. Update ADLC to never use `mkdir` — always use `write_file` to create files, which implicitly creates parent dirs. This is a workaround, not a proper fix — the system `mkdir` needs repair.

## Finding 2: Plan must auto-write embedded .ai/ on startup, not just embed (HIGH)
- **What's wrong**: The plan defines `Resolve()` but doesn't say who calls it. The agent discovers instructions via filesystem tools (read_file, list_dir, grep) which bypass agentkit entirely. Embedding alone is invisible.
- **Fix**: On startup, CLI must check if CWD/.ai/ exists. If not, auto-write embedded files from binary. This makes embedding actually reachable.

## Finding 3: Integration test must test the real scenario (HIGH)
- **What's wrong**: The plan's TestIntegration_NoLocalDir only tests `AgentInstructions()` returning content — it doesn't test the REAL scenario: build binary, copy to empty dir, run it, verify agent finds instructions.
- **Fix**: Add a test that builds the binary, runs it in an empty temp directory, and verifies .ai/ was auto-created.

## Finding 4: CLAUDE.md should NOT be embedded (MEDIUM)
- **What's wrong**: CLAUDE.md is read by Claude Code from disk at startup. Claude Code cannot read Go binary embedded files. Embedding CLAUDE.md is waste.
- **Fix**: Don't embed CLAUDE.md. Keep it on disk only.

## Finding 5: Version stamp needed (MEDIUM)
- **What's wrong**: Embedded instructions have no version. If a user has an old binary, there's no way to know the embedded instructions are stale.
- **Fix**: Embed a content hash (SHA256 of all embedded files) and expose it via agentkit.Version().

## Finding 6: Completeness guard missing (MEDIUM)
- **What's wrong**: Tests verify what IS embedded but not whether ALL source files are embedded. A new rule file added to .ai/rules/ but not added to //go:embed patterns would pass all tests.
- **Fix**: Add a test that walks the source .ai/ directory and verifies every file is present in the embedded FS.
