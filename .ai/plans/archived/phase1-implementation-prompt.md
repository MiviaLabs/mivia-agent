# Implementation Prompt — Phase 1

Copy and paste this entire block into the next mivia session:

```
Read .ai/plans/agent-self-work-regression-guardrails.md in full.

You are implementing Phase 1 (Create Artifacts) of this plan. Do the following **in order**, verifying each step before moving to the next:

### Step 1 — Create `.ai/invariants.md`

Create the invariant manifest from Section 4.1 of the plan. Use **exact** test function names — verify each one exists in the codebase before writing it.

Run this to find all existing test names before writing:
```
rg -n "func Test" internal/ -g "*_test.go" | grep -oP 'func \K\w+' | sort -u
```

Cross-reference every test name against that list. If a test doesn't exist, note it as a gap (don't invent names).

### Step 2 — Edit `.ai/rules/20-agent-quality.md`

Insert the "## Regression Tests" and "## Invariant Verification" sections from Section 4.2 of the plan. Place them after the existing "## Mutation Proofs" section and before "## Reviews".

After inserting, verify:
- The file still has valid markdown structure
- No duplicate section headings were created
- The existing Mutation Proofs and Reviews sections are intact

### Step 3 — Edit `.ai/agent-prompt.md`

Add this to the "How to orient in this repo" section (after the "Control surface" bullet, before "Verify *this* workspace"):

```
- **Invariant manifest** at `.ai/invariants.md` — before changing code in
  `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, or
  `internal/config/`, read the manifest and run the corresponding invariant tests.
  A change that breaks a named invariant is blocked.
```

### Step 4 — Edit `.ai/skills/engineering-working-contract/SKILL.md`

Find the "Execution" section (numbered list). Add as the last item (after "6. ..."):

```
7. Before committing changes to sensitive areas (cli, tools, agent, chat, config):
   a. Read `.ai/invariants.md` — identify which invariants cover your area.
   b. Run the corresponding invariant tests (see "Invariant Verification" in
      20-agent-quality.md).
   c. If any fail, restore the invariant or document the update with
      `Invariant-Update: INV-XXX <reason>`.
```

### Step 5 — Edit `Makefile`

Add these targets **before** the existing `.PHONY` line at the bottom:

```makefile
# Validate invariant manifest: every referenced test function must exist.
.PHONY: validate-invariants
validate-invariants:
	@echo "=== Validating invariant manifest ==="
	@failed=0; \
	while IFS= read -r line; do \
		testname=$$(echo "$$line" | sed -n 's/.*`\\(Test[A-Za-z_0-9]*\\)`.*/\\1/p'); \
		if [ -n "$$testname" ]; then \
			if ! rg -q "func $$testname\\b" internal/ -g "*_test.go" 2>/dev/null; then \
				echo "  MISSING: $$testname"; \
				failed=$$((failed + 1)); \
			fi; \
		fi; \
	done < .ai/invariants.md; \
	if [ "$$failed" -gt 0 ]; then \
		echo "FAIL: $$failed invariant test(s) not found"; \
		exit 1; \
	fi; \
	echo "  All invariant tests found ✓"

# Run full invariant test suite.
.PHONY: invariants
invariants: validate-invariants
	go test -run 'TestBridgePath|TestPollCmd|TestBridgeDrain|TestUIEventMsg|TestFinishStream|TestSmoke' ./internal/cli/ -count=1 -timeout=120s
	go test -run 'TestToolSurface|TestRedact|TestSearchOpenAI|TestSearchLocalSkipsSecret' ./internal/tools/ -count=1 -timeout=60s
	go test -run 'TestRedactTool|TestDelegateMultiStep|TestNewSessionDispatcherRegisters' ./internal/agent/ -count=1 -timeout=60s
	go test -run 'TestSendUser|TestConcurrent' ./internal/chat/ -count=1 -timeout=60s
	go test -run 'TestConfig|TestRedact' ./internal/config/ -count=1 -timeout=60s
	@echo "=== All invariants PASS ==="
```

### Step 6 — Verify Everything

Run these in order. **Any failure blocks the implementation — fix it before proceeding.**

```bash
make validate-invariants
go vet ./...
go build -o bin/mivia ./cmd/mivia
go test ./... -count=1 -timeout=180s
```

### Step 7 — Commit

After all verifications pass, commit with:
```
feat(ai): add regression guardrails — invariants, rules, hooks
```

The commit body must include:
```
Implements Phase 1 of .ai/plans/agent-self-work-regression-guardrails.md

Changes:
- .ai/invariants.md — system invariant manifest with exact test names
- .ai/rules/20-agent-quality.md — Regression Tests + Invariant Verification
- .ai/agent-prompt.md — invariant consultation instruction
- .ai/skills/engineering-working-contract/SKILL.md — invariant step in Execution
- Makefile — validate-invariants and invariants targets
```

Do NOT modify hooks or commit-msg checks in this phase — that's Phase 2.
```
