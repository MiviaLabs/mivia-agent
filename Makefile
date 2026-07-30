SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

BINARY := mivia
CMD_PKG := ./cmd/mivia

.PHONY: help install-hooks hooks verify verify-agent pre-commit pre-push \
	secret-scan docs-check semgrep semgrep-validate semgrep-test \
	hook-test agent-hook-test structure-check commit-check go-check test race vet build tidy fmt fmt-check \
	validate-invariants invariants mutation-coverage

help:
	@printf '%s\n' \
		'Targets:' \
		'  make install-hooks     Install repo Git hooks for this clone' \
		'  make verify            Run all offline local quality gates' \
		'  make verify-agent      Validate agent adapter surface' \
		'  make validate-invariants  Verify all test refs in .mivia/invariants.md exist' \
		'  make invariants        Run all invariant tests (TUI, agent, security)' \
		'  make pre-commit        Run the committed pre-commit hook' \
		'  make pre-push          Run the committed pre-push hook' \
		'  make secret-scan       Scan working tree for secrets (offline)' \
		'  make docs-check        Check adapter/docs ownership' \
		'  make structure-check   Go LOC/function limits + 500 KiB file-size' \
		'  make semgrep           Run repo Semgrep policy scan (if installed)' \
		'  make semgrep-validate  Validate Semgrep config (if installed)' \
		'  make semgrep-test      Run Semgrep rule contract tests' \
		'  make hook-test         Run Git hook contract tests' \
		'  make agent-hook-test   Run agent hook guard contract tests' \
		'  make go-check          gofmt + test + vet + build' \
		'  make test              go test ./...' \
		'  make invariants        Run invariant tests (TUI, agent, security)' \
		'  make mutation-coverage Explore mutation test readiness for core packages' \
		'  make race              go test -race ./...' \
		'  make vet               go vet ./...' \
		'  make build             Build binary $(BINARY) from $(CMD_PKG)' \
		'  make tidy              go mod tidy' \
		'  make fmt               gofmt -w tracked Go files'

install-hooks hooks:
	@scripts/install_git_hooks.sh

# Offline gates only — no network required beyond local tool installs.
verify: verify-agent docs-check secret-scan structure-check \
	semgrep-validate semgrep-test hook-test agent-hook-test \
	validate-invariants semgrep go-check

verify-agent:
	@python3 scripts/verify_agent_config.py

validate-invariants:
	@echo "Validating invariant test references in .mivia/invariants.md..."
	@python3 scripts/test_validate_invariants.py
	@python3 scripts/validate_invariants.py

docs-check:
	@scripts/docs-check

secret-scan:
	@scripts/secret-scan

structure-check:
	@python3 scripts/git-hooks/file-size-check --tracked
	@python3 scripts/check_go_structure.py --strict --all

commit-check:
	@python3 scripts/git-hooks/check-commit-subject "$(MSG)"
	@python3 scripts/test_go_structure.py

semgrep-validate:
	@if command -v semgrep >/dev/null 2>&1; then \
		out="$$(semgrep --validate --config semgrep/agent-standards.yml 2>&1)" || true; \
		printf '%s\n' "$$out"; \
		if ! printf '%s' "$$out" | grep -q 'Configuration is valid'; then \
			printf 'semgrep-validate: configuration invalid\n' >&2; \
			exit 1; \
		fi; \
	else \
		printf 'semgrep not installed; skipping semgrep-validate\n'; \
	fi

semgrep-test:
	@python3 scripts/test_semgrep_rules.py

semgrep:
	@if command -v semgrep >/dev/null 2>&1; then \
		semgrep --config semgrep/agent-standards.yml --error --skip-unknown-extensions --metrics off --disable-nosem .; \
	else \
		printf 'semgrep not installed; skipping semgrep\n'; \
	fi

hook-test:
	@python3 scripts/test_git_hooks.py

agent-hook-test:
	@python3 scripts/test_agent_hook_guard.py
	@python3 scripts/test_docs_ownership.py
	@python3 scripts/test_secret_scan.py

pre-commit:
	@.githooks/pre-commit

pre-push:
	@.githooks/pre-push

fmt:
	@mapfile -t files < <(git ls-files '*.go' 2>/dev/null || true); \
	if (($${#files[@]}==0)); then mapfile -t files < <(find cmd internal -name '*.go' 2>/dev/null || true); fi; \
	if (($${#files[@]})); then gofmt -w "$${files[@]}"; fi

fmt-check:
	@mapfile -t files < <(git ls-files '*.go' 2>/dev/null || true); \
	if (($${#files[@]}==0)); then mapfile -t files < <(find cmd internal -name '*.go' 2>/dev/null || true); fi; \
	if (($${#files[@]})); then \
		unformatted="$$(gofmt -l "$${files[@]}")"; \
		if [[ -n "$$unformatted" ]]; then \
			printf 'gofmt required for:\n%s\n' "$$unformatted" >&2; \
			exit 1; \
		fi; \
	fi

go-check: fmt-check
	@go test ./...
	@go vet ./...
	@go build -o $(BINARY) $(CMD_PKG)

test:
	@go test ./...

invariants:
	@echo "Running all invariant tests..."
	@go test -run 'TestBridge|TestTuiTickMsg|TestFinishStream|TestPollCmd|TestUIEventMsg|TestTUISmoke|TestStreamBridge|TestSearchOpenAI|TestToolSurface|TestDelegateToolMultiStep|TestRedactToolInput|TestMultiStepHandler|TestLoopRejectsDispatcherToolMissingFromVisibleRegistry|TestSearchLocalSkips|TestGrepNestedAndGlob|TestIsSecretPath|TestBlockEnvRead|TestSessionMessages|TestPrivacyRedact|TestPromptGeneric|TestGenericSurface|TestTuiTickMsgStress|TestStreamBridgeConcurrent|TestBridgeConcurrent|TestEmptyContentTools|TestShortInterim|TestInterimAssistantBecomesChatBubble|TestCancelKeeps|TestCancelBefore|TestInterimRejected|TestInterimAccepted|TestPushInterimGates|TestShouldFollow|TestAwaitingFirst|TestToolStatusLine|TestToolVerbMap|TestFollowPreserves|TestNoteUserScrolled|TestJumpToLatest|TestCancelThenTurnEnd|TestReconstructStatus|TestClassicUI|TestWorkGroup|TestFindWorkGroups|TestScrollAccept|TestScrollProg|TestScrollPTY|TestMouseAvailable|TestNewTUIModel_Mouse|TestScrollIndicator_Glyph|TestPaintRaster|TestRunHandle|TestResumeRefusesRunHeldByAnotherExecutor|TestResumeReleasesClaimOnError|TestClaimReleasedOnRunCompletion|TestClaimReleasedAfterHolderClose|TestSpawn|TestMemoryBackendClaimIsExclusive|TestCancelRunCannotCancelForeignRun|TestUnauthorizedAndUnknownAreIndistinguishable|TestTaskDepthPropagates|TestRunID|TestModelVisibleOutputRefResolves|TestModelVisibleErrorRefResolves|TestReferenceHasSingleMinter|TestStoreContentFailureBlocksRef|TestResultReferencesUseCanonicalFullDigest|TestLedgerRead|TestListRunEvents|TestLedgerToolsAreUnprivilegedAndReachSubAgents|TestListEventsRestoresKindAfterProjectionRebuild|TestSessionToolSurface|TestLedgerReference|TestLedgerParseReference|TestLedgerMalformedReference|TestModelVisibleRefsOmittedWhenContentWriteFailed|TestModelVisibleRefsUseRecordedValue|TestStoredResultRefsFallsBackToCanonicalMinting|TestDispatcherFailureOmitsUnstoredRefs|TestDispatchTasksErrorEnvelopeOmitsUnstoredReference|TestDelegateReturnsOutputWhenContentStoreFails|TestRecoveredFailedTaskWithoutRefStillReportsError|TestListEventsToleratesUndecodablePayload|TestListEventsPreserveOriginalTimestampAcrossRebuild|TestAppendEventStampsBeforeMarshalling|TestListEventsOrderedBySequenceUnderTiedTimestamps|TestLegacyRowWithoutTimestampFallsBackToReadInstant|TestMemoryCreateRunPreservesSuppliedCreatedAt|TestMemoryCreateRunStampsWhenUnstamped|TestMemoryAppendEventStampsOnlyUnstampedEvents|TestProjectionStateIncludesTimestampsAcrossRebuild|TestDeletedRunDoesNotResurrectInNextProcess|TestDeleteRunKeepsChangesCursorMonotonic|TestDeleteRunConvergesInASecondReader|TestDeleteRunAllowsSameIDToBeRecreatedAndCaughtUp|TestDeleteRunLeavesContentUntouched|TestDeleteRunOnMemoryBackend|TestRecoverDoesNotReportDeletedRunAsInterrupted|TestSharedContentRefSurvivesOneRunDeletion|TestContentStoreIsNeverReclaimed|TestTruncateUTF8|TestLoadMarkdown|TestPromptRendersFromDefinitionTriggers|TestNoHardcodedLegacyNamespace|TestResume|TestNoMessageLoss|TestLoopFallsBack|TestWelcomeJKNav|TestWelcomeCtrlD|TestToolPanel|TestSlashHelp|TestCtrlMDoesNotToggleMouse|TestPaste|TestMultilinePaste|TestClipboardRead|TestCtrlVFailure|TestCopyAck|TestCopyLargeText|TestCopyRightClickAck|TestYankKeyCopies|TestCtrlCCopiesOnly|TestRightClickCopies|TestOSC52|TestCtrlCCopies|TestCtrlCClears|TestCtrlCArms|TestCtrlCArm|TestCtrlCDuring|TestSelectMode|TestCtrlEIsLineEnd|TestSelectSlash|TestWelcomeCtrlQ|TestIntegration_QuitAfterCancel|TestKeyRegistry|TestHelpIsGenerated|TestRegisteredChatKeys|TestForbiddenKeys|TestRunDashboard|TestInterruptedRunReport|TestFormatListedRuns|TestRecoverClassifies|TestDispatcherFail|TestDispatchTasksHangingTask|TestDispatchOrchestrationBudget|TestPoolCancelStillReports|TestPoolRecordsBlockedTask|TestAnalyzer|TestReferences|TestFindReferences|TestSameObject' ./... -count=1 -timeout=180s
	@echo ""
	@python3 scripts/invariant_coverage.py

mutation-coverage:
	@python3 scripts/mutation_coverage.py

race:
	@go test -race ./...

vet:
	@go vet ./...

build:
	@go build -o $(BINARY) $(CMD_PKG)

tidy:
	@go mod tidy
