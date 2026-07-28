# Tasks: bug-audit-all-phases

## Wave 1 — Parallel hostile audit (4 agents)
### t1: Audit Phase 0-1 — Contracts + Coordinator
- **Type**: audit
- **Files**: internal/ledger/{repository,types,transition,displayname,memory}.go, internal/coordinator/{coordinator,cancel,handle_lifecycle,record_results,recovery,validation}.go
- **Verification**: Write findings to `.ai/plan/bug-audit-all-phases/audit/round-01-p0-1.md`
- **Timeout**: 120s
- **Context scope**: all files listed above

### t2: Audit Phase 2 — Orchestration Tools
- **Type**: audit
- **Files**: internal/cli/orchestrate.go, internal/cli/orchestrate_lifecycle.go
- **Verification**: Write findings to `.ai/plan/bug-audit-all-phases/audit/round-01-p2.md`
- **Timeout**: 120s
- **Context scope**: all files listed above

### t3: Audit Phase 3 — Durable Persistence
- **Type**: audit
- **Files**: internal/ledger/{storage,storage_schema}.go, internal/storage/store.go
- **Verification**: Write findings to `.ai/plan/bug-audit-all-phases/audit/round-01-p3.md`
- **Timeout**: 120s
- **Context scope**: all files listed above

### t4: Audit Phase 4 — Observability + Subagents
- **Type**: audit
- **Files**: internal/events/{metrics,bus,event}.go, internal/cli/{diagnostics,tui_run}.go, internal/subagents/{subagents,multi_step,oneshot}.go
- **Verification**: Write findings to `.ai/plan/bug-audit-all-phases/audit/round-01-p4.md`
- **Timeout**: 120s
- **Context scope**: all files listed above
