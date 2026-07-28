# Tasks: embed-agent-instructions

## Wave 1 — agentkit embed.go (RED test + skeleton)
### t1a: agentkit RED test
- **File**: `internal/agentkit/embed_test.go` (create)
- **Type**: test
- **Depends on**: none (saved as pending, won't compile until t1b)
- **Verification**: Saved pending. Orchestrator verifies test content manually before t1b.

### t1b: agentkit type skeleton
- **File**: `internal/agentkit/embed.go` (create) + `internal/agentkit/agentkit.go` (create)
- **Type**: prod (skeleton)
- **Depends on**: t1a
- **API**: embed.go has `//go:embed` directives for AGENTS.md + .ai/*. agentkit.go has stub functions returning zero values.
- **Verification**: `go build ./internal/agentkit/...` succeeds. Then `go test -run TestEmbed_ ./internal/agentkit/...` FAILS with assertion errors (RED).

## Wave 2 — agentkit full implementation
### t2: agentkit full implementation
- **File**: `internal/agentkit/embed.go` (modify) + `internal/agentkit/agentkit.go` (modify)
- **Type**: prod
- **Depends on**: t1b
- **API**: Full implementations: AgentInstructions, Rule, Doctrine, Skill, WriteInstructions, HasLocalOverride, EnsureInstructions, Resolve, Version
- **Verification**: `go test -run TestEmbed_ ./internal/agentkit/...` ALL PASS

### t3: agentkit full tests
- **File**: `internal/agentkit/embed_test.go` (modify — expand)
- **Type**: test
- **Depends on**: t2
- **Verification**: `go test -count=1 ./internal/agentkit/...` ALL PASS

## Wave 3 — Wire into main.go
### t4: Wire auto-write on startup
- **File**: `cmd/mivia/main.go` (modify)
- **Type**: prod
- **Depends on**: t2
- **Changes**: Call agentkit.EnsureInstructions(CWD) at startup. Add --init-agent-dir flag.
- **Verification**: `go build ./cmd/mivia/...` succeeds. `go test -count=1 ./cmd/mivia/...` passes.

## Wave 4 — Fix ADLC
### t5: Remove mkdir from ADLC
- **File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md` (modify)
- **Type**: prod
- **Depends on**: none
- **Changes**: Replace `mkdir -p` in Step 0 with `write_file` (which auto-creates parent dirs).
- **Verification**: File reads correctly.

## Wave 5 — Integration test
### t6: Integration test — binary in empty dir
- **File**: `internal/agentkit/embed_test.go` (modify — add)
- **Type**: integration
- **Depends on**: t4
- **Test**: Build binary, copy to empty dir, run with --init-agent-dir, verify .ai/ created.
- **Verification**: `go test -run TestIntegration_BinaryInEmptyDir ./internal/agentkit/...` passes.

## Wave 6 — Final review
### t7: Full verification
- **File**: review of all changes
- **Type**: review
- **Depends on**: t6
- **Verification**: `go build ./... && go vet ./... && go test -race ./...` ALL PASS
