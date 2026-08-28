package clichat

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ContextStorePath is always SQLite-backed regardless of [subagents]
// store_backend, which governs the separate orchestration ledger
// (internal/cli/orchestration_state.go), not this store.
//
// NOTE: openContextStore/openContextStorePath below were NOT relocated to
// internal/composition as the task briefing directed. That briefing assumed
// internal/composition already imports internal/cli; it does not (verified:
// zero cli imports in internal/composition/*.go), while internal/cli already
// imports internal/composition in 5 files (dispatcher.go, hooks_runner.go,
// mcp_session.go, chat_workspace.go, workflow_authority.go). Moving these two
// functions to composition and having composition call cli.ContextStorePath
// would close cli -> composition -> cli, a compiler-rejected import cycle.
// Flagged as a blocker; these two functions stay in internal/cli unexported,
// same as before this slice, only ContextStorePath was exported (cliworktree
// needs it externally; the other two do not).
func ContextStorePath(root string, cfg config.SubagentConfig) string {
	if cfg.StorePath != "" {
		path := config.ExpandPath(cfg.StorePath)
		// A relative store_path (e.g. the dogfooded ".mivia/context.db") must
		// resolve against the workspace root, not the process's current
		// working directory - the same rule repositorySessionStorePath
		// already applies (chat_repository_binding.go). Without this, every
		// direct ContextStorePath caller (workflow admission/resume/reconcile
		// in internal/cliworkflow, stack_admit.go, stack_drive.go) opens a
		// different on-disk database than the one a live chat session
		// resolved through repositorySessionStorePath, silently forking
		// sessions and workflow runs across two SQLite files depending on
		// which resolver a given call site used and where the process
		// happened to be launched from.
		if !filepath.IsAbs(path) {
			return filepath.Join(root, path)
		}
		return path
	}
	return workspace.GlobalContextStorePath(root)
}

func openContextStore(root string, cfg config.SubagentConfig) (*storage.SQLite, error) {
	p := ContextStorePath(root, cfg)
	return openContextStorePathWithOptions(p, storage.Options{Harden: hardenOrchestrationStore(root, p)})
}

// hardenOrchestrationStore reports whether path names the ad-hoc,
// temp-dir-backed orchestration store (config.TempStorePath), the one tier
// whose directory no operator manages. Only that tier is chmod'd; an
// operator-configured store_path keeps the modes its owner chose.
func hardenOrchestrationStore(root, path string) bool {
	return sameFilePath(runtime.GOOS, path, config.TempStorePath(root, "orchestration"))
}

// openOrchestrationStoreAt opens the orchestration ledger at path, hardening
// the ad-hoc temp tier (see hardenOrchestrationStore). Callers that already
// hold the same store open pass it by instead.
func openOrchestrationStoreAt(root, path string) (*storage.SQLite, error) {
	return storage.OpenSQLiteWithOptions(path, storage.Options{Harden: hardenOrchestrationStore(root, path)})
}

func openContextStorePath(path string) (*storage.SQLite, error) {
	return openContextStorePathWithOptions(path, storage.Options{})
}

func openContextStorePathWithOptions(path string, opts storage.Options) (*storage.SQLite, error) {
	store, err := storage.OpenSQLiteWithOptions(path, opts)
	if err != nil {
		return nil, fmt.Errorf("open context store %q: %w", path, err)
	}
	return store, nil
}

func sameFilePath(goos, a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = normalizeFilePath(a), normalizeFilePath(b)
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func normalizeFilePath(p string) string {
	return path.Clean(strings.ReplaceAll(p, `\`, "/"))
}
