SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

BINARY := mivia
CMD_PKG := ./cmd/mivia

# Build provenance injected into internal/version at link time so `--version`
# identifies the exact commit (and whether the tree was dirty) a binary was
# built from. VERSION_LDFLAGS is quoted at the call site; the injected values
# are hex literals and the fixed strings "dirty"/"clean", never raw
# `git status` output, so the -ldflags argument stays POSIX-shell-safe.
# NOTE: the -X target must be the FULL package import path - a bare
# "internal/version.Commit" does not match the linker's symbol table, so the
# override is silently dropped and --version falls back to 0.0.0-dev.
# GOWORK=off: under a multi-module go.work, a bare `go list -m` prints EVERY
# workspace module (one per line), which turns the -X argument into a
# malformed multi-word flag and fails the link. This repo's own module path
# comes from its own go.mod alone.
VERSION_PKG := $(shell GOWORK=off go list -m)/internal/version
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DIRTY := $(shell test -n "$$(git status --porcelain 2>/dev/null)" && echo dirty || echo clean)
VERSION_LDFLAGS := -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Dirty=$(DIRTY)

.PHONY: help install-hooks hooks verify verify-agent pre-commit pre-push \
	secret-scan docs-check semgrep semgrep-validate semgrep-test \
	hook-test agent-hook-test test-quality structure-check import-layers-check timeout-saturation-check commit-check go-check verify-go test test-changed race vet build tidy fmt fmt-check \
	validate-invariants invariants mutation diff-coverage verifier-integration smoke release release-test \
	prose-check

help:
	@printf '%s\n' \
		'Targets:' \
		'  make install-hooks     Install repo Git hooks for this clone' \
		'  make verify            Run all offline local quality gates' \
		'  make verify-agent      Validate agent adapter surface' \
		'  make test-quality      Inspect Go test quality and anti-fake-work gates' \
		'  make validate-invariants  Verify all test refs in .mivia/invariants.md exist' \
		'  make invariants        Run all invariant tests (TUI, agent, security)' \
		'  make pre-commit        Run the committed pre-commit hook' \
		'  make pre-push          Run the committed pre-push hook' \
		'  make secret-scan       Scan working tree for secrets (offline)' \
		'  make docs-check        Check adapter/docs ownership' \
		'  make structure-check   Go LOC/function limits + 500 KiB file-size' \
		'  make import-layers-check  Internal import-edge policy (allow/deny/cap)' \
		'  make timeout-saturation-check  No unbounded seconds-to-Duration multiply (DC-7)' \
		'  make semgrep           Run repo Semgrep policy scan (if installed)' \
		'  make semgrep-validate  Validate Semgrep config (if installed)' \
		'  make semgrep-test      Run Semgrep rule contract tests' \
		'  make hook-test         Run Git hook contract tests' \
		'  make agent-hook-test   Run agent hook guard contract tests' \
		'  make go-check          gofmt + test + vet + build' \
		'  make verify-go         go-check + diff-coverage over ONE instrumented suite run' \
		'  make test              go test ./...' \
		'  make invariants        Run invariant tests (TUI, agent, security)' \
		'  make mutation           Run a real mutation sweep (PKG=internal/... required)' \
		'  make prose-check        Scan for leaked audit labels, banned names, and prose gates (informational)' \
		'  make diff-coverage    Self-test the gate, then fail if changed Go lines are untested' \
		'  make race              go test -race ./...' \
		'  make vet               go vet ./...' \
		'  make build             Build binary $(BINARY) from $(CMD_PKG)' \
		'  make release           Build release archives + checksums into dist/' \
		'  make release-test      Check release and installer contracts' \
		'  make tidy              go mod tidy' \
		'  make fmt               gofmt -w tracked Go files' \
		'  make smoke             Fast workflow-engine smoke suite'

install-hooks hooks:
	@scripts/install_git_hooks.sh

# Offline gates only - no network required beyond local tool installs.
#
# The Go suite runs ONCE here, instrumented, and the coverage gate reads that
# same profile (verify-go). Previously verify ran the whole suite three times -
# go-check, the verifier sandbox profile, and diff-coverage - for about six
# minutes, of which five and a half were the same tests over and over.
# verifier-integration is no longer a verify prerequisite: its sandbox test
# runs the entire repository test profile a second time inside a sandbox. It
# still runs on main and macOS in CI, and standalone via `make
# verifier-integration`.
verify: verify-agent docs-check release-test secret-scan structure-check \
	import-layers-check timeout-saturation-check semgrep-validate semgrep-test \
	hook-test agent-hook-test test-quality validate-invariants semgrep verify-go
	@python3 scripts/check_mutation.py --probe
	@python3 scripts/check_mutation.py --staged

verify-agent: agents-check
	@python3 scripts/verify_agent_config.py

test-quality:
	@echo "Checking test quality and fake-test prevention..."
	@python3 scripts/test_check_test_quality.py
	@python3 scripts/check_test_quality.py --diff

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

import-layers-check:
	@python3 scripts/check_import_layers.py

timeout-saturation-check:
	@python3 scripts/check_timeout_saturation.py --probe
	@python3 scripts/test_check_timeout_saturation.py
	@python3 scripts/check_timeout_saturation.py

commit-check:
	@python3 scripts/git-hooks/check-commit-subject "$(MSG)"
	@python3 scripts/test_go_structure.py

semgrep-validate:
	@if command -v semgrep >/dev/null 2>&1; then \
		out="$$(semgrep --validate --config semgrep/agent-standards.yml -j 1 2>&1)" || true; \
		printf '%s\n' "$$out"; \
		if printf '%s' "$$out" | grep -q 'Configuration is valid'; then \
			printf 'semgrep-validate: ok\n'; \
		elif printf '%s' "$$out" | grep -qE 'semgrep-core exited with|Uncaught exn in Core_scan\.scan|engine was killed|Cannot allocate memory io_uring_queue_init'; then \
			printf 'semgrep-validate: engine unavailable (io_uring/memory); skipped\n' >&2; \
		else \
			printf 'semgrep-validate: configuration invalid\n' >&2; \
			exit 1; \
		fi; \
	else \
		printf 'semgrep not installed; skipping semgrep-validate\n'; \
	fi

semgrep-test:
	@python3 scripts/test_semgrep_rules.py
	@python3 scripts/check_semgrep_probes.py

# Bound worker domains because default per-CPU workers can fail with
# io_uring_queue_init (ENOMEM) under a low RLIMIT_MEMLOCK.
semgrep:
	@if command -v semgrep >/dev/null 2>&1; then \
		out="$$(semgrep --config semgrep/agent-standards.yml --error --skip-unknown-extensions --metrics off --disable-nosem -j 2 . 2>&1)"; rc=$$?; \
		printf '%s\n' "$$out"; \
		if [ "$$rc" -ne 0 ] && printf '%s' "$$out" | grep -qE 'semgrep-core exited with|Uncaught exn in Core_scan\.scan|engine was killed|Cannot allocate memory io_uring_queue_init'; then \
			printf 'semgrep: engine unavailable (io_uring/memory); scan skipped\n' >&2; \
		else \
			exit "$$rc"; \
		fi; \
	else \
		printf 'semgrep not installed; skipping semgrep\n'; \
	fi

hook-test:
	@python3 scripts/test_git_hooks.py

agent-hook-test:
	@python3 scripts/test_agent_hook_guard.py
	@python3 scripts/test_docs_ownership.py
	@python3 scripts/test_check_provider_docs.py
	@python3 scripts/test_secret_scan.py
	@python3 scripts/test_check_mutation.py
	@python3 scripts/test_check_labels.py
	@python3 scripts/test_check_names.py
	@python3 scripts/test_check_prose.py
	@python3 scripts/test_import_layers.py

pre-commit:
	@.githooks/pre-commit

pre-push:
	@.githooks/pre-push

fmt:
	@files=(); while IFS= read -r file; do [[ -f "$$file" ]] && files+=("$$file"); done < <(git ls-files '*.go' 2>/dev/null || true); \
	if (($${#files[@]}==0)); then while IFS= read -r file; do [[ -f "$$file" ]] && files+=("$$file"); done < <(find cmd internal -name '*.go' 2>/dev/null || true); fi; \
	if (($${#files[@]})); then gofmt -w "$${files[@]}"; fi

fmt-check:
	@files=(); while IFS= read -r file; do [[ -f "$$file" ]] && files+=("$$file"); done < <(git ls-files '*.go' 2>/dev/null || true); \
	if (($${#files[@]}==0)); then while IFS= read -r file; do [[ -f "$$file" ]] && files+=("$$file"); done < <(find cmd internal -name '*.go' 2>/dev/null || true); fi; \
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
	@go build -ldflags "$(VERSION_LDFLAGS)" -o $(BINARY) $(CMD_PKG)

# verify-fast is the Go-only subset of verify: gofmt + vet + tests + build +
# diff-coverage, without the agent-config / docs / secrets / structure /
# import-layers / semgrep / hook-test gates. Use it for tight iteration when
# the only thing that changed is Go code; the full `make verify` still runs
# on every push.
verify-fast: verify-go

# agents-check validates the four subagent role files under
# .agents/agents/: required frontmatter keys, name matches filename, tools
# list non-empty, and a Disallowed operations section is present. The
# script is stdlib-only and exits non-zero with the exact failure list.
agents-check:
	@python3 scripts/check_agents.py

# skills-move is a one-time migration target: when the canonical skill
# home moves (today from .mivia/skills/ to .agents/skills/), this target
# performs the copy, verifies the destination, and removes the source.
# It is idempotent: running it twice when the source is already gone is
# a clean no-op. After the migration lands, this target stays as the
# documented procedure if the home ever has to move again.
skills-move:
	@src=.mivia/skills; dst=.agents/skills; claude_dst=.claude/skills; \
	if [ ! -d "$$src" ]; then \
		echo "skills-move: $$src already absent, nothing to do"; \
	else \
		mkdir -p "$$dst" "$$claude_dst"; \
		for d in "$$src"/*/; do \
			[ -d "$$d" ] || continue; \
			name=$$(basename "$$d"); \
			rm -rf "$$dst/$$name" "$$claude_dst/$$name"; \
			cp -r "$$d" "$$dst/$$name"; \
			cp -r "$$d" "$$claude_dst/$$name"; \
		done; \
		rm -rf "$$src"; \
		echo "skills-move: copied $$(ls $$dst | wc -l) skill(s) to $$dst and $$claude_dst"; \
	fi

# verify-go is go-check plus the diff-coverage gate over ONE instrumented run
# of the suite. The two used to be separate full runs of the same tests: an
# uninstrumented `go test ./...` and then diff-coverage's own -count=1
# coverage run. Instrumenting costs almost nothing next to running the tests
# (measured: 114s with -coverpkg=./... against 121s scoped to one package), so
# one instrumented run replaces both.
#
# -count=1 is load-bearing and must not be dropped: `go test` caches coverage
# output, and a replayed entry carries the block coordinates of the source it
# was recorded against, so after an edit shifts line numbers the gate reports
# uncovered lines that no test can fix. scripts/diff_coverage.py documents this
# at run_coverage_profile.
verify-go: fmt-check
	@profile="$$(mktemp -t mivia-verify-cover-XXXXXX.out)"; \
	trap 'rm -f "$$profile"' EXIT; \
	go test ./... -count=1 -coverpkg=./... -coverprofile="$$profile"; \
	go vet ./...; \
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BINARY) $(CMD_PKG); \
	$(MAKE) --no-print-directory diff-coverage DIFF_COVERAGE_PROFILE="$$profile"

test:
	@go test ./...

smoke:
	@go test ./internal/workflows/... -count=1 && go test ./internal/cli -run 'Workflow' -count=1

verifier-integration:
	@go test -tags=integration ./internal/workflows/definition

invariants:
	@echo "Running all invariant tests dynamically from .mivia/invariants.md..."
	@python3 scripts/validate_invariants.py --run
	@echo ""
	@python3 scripts/invariant_coverage.py

# Real mutation sweep against one package (slow: builds+tests a mutant per
# site). PKG is required, e.g. `make mutation PKG=internal/agent`. Exploratory
# by default (no floor); `--all-core` sweeps the default CORE_PACKAGES set.
# The fast self-test (planted fixtures, no real sweep) runs in `make verify`
# via `check_mutation.py --probe`.
mutation:
	@python3 scripts/check_mutation.py --pkg $(PKG)

mutation-check:
	@echo "Checking mutation kill-rate floors across configured packages in .mivia/policy/mutation/..."
	@python3 scripts/check_mutation.py --check-floors


# Informational, not part of `make verify`: check_labels.py and check_prose.py
# currently flag pre-existing content (this repo's docs legitimately embed
# durable correction/decision-reference IDs like C1/S3/INV-AG-12, and several
# docs exceed the 25-word sentence cap), and check_names.py flags legitimate
# domain vocabulary (PanelPhase, Backup(), versioned schema files) alongside
# real hits. Run standalone to see current findings; wiring into verify is
# blocked on a cleanup pass and tighter false-positive scoping.
prose-check:
	@python3 scripts/check_labels.py
	@python3 scripts/check_names.py
	@python3 scripts/check_prose.py

# DIFF_COVERAGE_PROFILE lets a caller that already ran the instrumented suite
# (verify-go) hand its profile over instead of paying for a second full run.
# Unset, the gate runs the suite itself, so `make diff-coverage` standalone is
# unchanged.
diff-coverage: DIFF_COVERAGE_PROFILE ?=
diff-coverage:
	@python3 scripts/test_diff_coverage.py
	@if [ -n "$(DIFF_COVERAGE_PROFILE)" ]; then \
		profile_arg="--profile $(DIFF_COVERAGE_PROFILE)"; \
	else \
		profile_arg=""; \
	fi; \
	BASE_REF="$$(git merge-base HEAD '@{upstream}' 2>/dev/null \
		|| git merge-base HEAD origin/main 2>/dev/null \
		|| git merge-base HEAD origin/master 2>/dev/null \
		|| true)"; \
	if [ -n "$$BASE_REF" ]; then \
		python3 scripts/diff_coverage.py --base "$$BASE_REF" $$profile_arg; \
	else \
		echo "diff-coverage: no upstream/origin base ref found; checking staged changes instead"; \
		python3 scripts/diff_coverage.py --staged $$profile_arg; \
	fi

race:
	@go test -race ./...

vet:
	@go vet ./...

build:
	@go build -ldflags "$(VERSION_LDFLAGS)" -o $(BINARY) $(CMD_PKG)

release:
	@scripts/release.sh

release-test:
	@python3 scripts/test_release.py
	@python3 scripts/test_installers.py

tidy:
	@go mod tidy

# test-changed runs the tests of every package that has an uncommitted or
# staged Go change. The commit hook runs an invariant subset only, so a package
# can break and still commit; this is the check that catches it before the push
# gate does, minutes later.
test-changed:
	@pkgs=$$(git diff --name-only --diff-filter=ACMR HEAD -- '*.go' \
	  | xargs -r -n1 dirname | sort -u | sed 's|^|./|'); \
	if [ -z "$$pkgs" ]; then echo "test-changed: no changed Go packages"; exit 0; fi; \
	echo "test-changed: $$pkgs"; \
	go test -count=1 -timeout=900s $$pkgs
