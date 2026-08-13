package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// wireSessionMemory wires the memory store into the session tool options so
// memory_save and memory_search register (plan 68). The org identity was
// resolved at config load from the user config file only; a workspace config
// cannot name the org store. res.Memory is the fully resolved [memory]
// section; a nil res leaves memory unwired.
func wireSessionMemory(opts *tools.DefaultOptions, root string, res *config.Resolved) error {
	if res == nil || !res.Memory.IsEnabled() {
		return nil
	}
	store, err := openMemoryStore(root, res.Memory)
	if err != nil {
		return fmt.Errorf("memory store: %w", err)
	}
	opts.Memory = store
	return nil
}

// openMemoryStore builds the session memory backend from the resolved
// [memory] config, read-write.
//
// store_path resolves relative to the workspace root, so a repo owner can
// keep the database inside the repository (for example ".mivia/memory.db")
// and commit it to transport memories with the repo; "~" expands to the home
// directory. Relative paths must stay inside the workspace: ".." segments are
// rejected so a repo-controlled config cannot route project writes to
// user-level files. Absolute paths (including ~-expanded ones) are allowed
// and are treated as repo-controlled config, like lifecycle hooks. The org
// store is user-level and fixed for v1.
//
// The returned store lives for the session (process lifetime for the CLI);
// every save runs a WAL checkpoint, so the main database file is always
// current and safe to commit at any time.
func openMemoryStore(root string, mc config.MemoryConfig) (memory.Store, error) {
	return openMemoryStoreWithReadOnly(root, mc, false)
}

// openMemoryStoreReadOnly is openMemoryStore with ReadOnly: true: a search
// session that must never write the committed database file.
func openMemoryStoreReadOnly(root string, mc config.MemoryConfig) (memory.Store, error) {
	return openMemoryStoreWithReadOnly(root, mc, true)
}

// openMemoryStoreWithReadOnly resolves the [memory] config to a memory store;
// readOnly controls memory.Config.ReadOnly (see openMemoryStore).
func openMemoryStoreWithReadOnly(root string, mc config.MemoryConfig, readOnly bool) (memory.Store, error) {
	projectPath := strings.TrimSpace(mc.StorePath)
	if projectPath == "" {
		projectPath = workspace.MemoryDBPath(root)
	} else {
		projectPath = config.ExpandPath(projectPath)
		if !filepath.IsAbs(projectPath) {
			cleaned := filepath.Clean(projectPath)
			if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("memory store_path %q escapes the workspace root", mc.StorePath)
			}
			projectPath = filepath.Join(root, cleaned)
		}
	}
	cfg := memory.Config{
		Backend:          mc.StoreBackend,
		ProjectPath:      projectPath,
		OrgPath:          workspace.OrgMemoryDBPath(),
		OrgID:            mc.OrgID,
		MaxEntryBytes:    mc.MaxEntryBytes,
		MaxEntries:       mc.MaxEntries,
		MaxSearchResults: mc.MaxSearchResults,
		BlockPatterns:    mc.BlockPatterns,
		ReadOnly:         readOnly,
	}
	return memory.Open(cfg)
}

// coreMemoryBlock builds the auto-injected system-prompt block (D1). It is a
// true no-op ("") when core-tier injection is disabled or the scope has no
// core entries - composeSystemPrompt (internal/chat) treats an empty block
// as "return base unchanged," so this function is the only place that
// decides whether injection happens at all. Errors reading the store are
// swallowed to "": a broken query must never break session startup, and the
// pull-based memory_search tool remains available regardless.
func coreMemoryBlock(ctx context.Context, store memory.Store, scope memory.Scope, mc config.MemoryConfig) string {
	if !mc.InjectCore || store == nil {
		return ""
	}
	entries, err := store.CoreEntries(ctx, scope)
	if err != nil || len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(e.Title)
		b.WriteString(": ")
		b.WriteString(e.Snippet)
	}
	return b.String()
}

// coreMemoryBlockForState is coreMemoryBlock scoped to project-only
// injection (plan 77, E4 - ScopeAll is invalid for CoreEntries, org-scope
// injection is a deferred decision), reading state's session-lifetime
// store. Callers hold state.mu (matching the LedgerRepo/Memory field
// convention), so the field read is direct. context.Background() is used
// deliberately: every call site is a session-init or turn-boundary prompt
// rebuild with no request-scoped context threaded through this call chain,
// and CoreEntries is a single indexed, 24-row-capped query - not worth
// plumbing a context through half a dozen signatures for.
func coreMemoryBlockForState(state *agentSessionState) string {
	if state == nil {
		return ""
	}
	return coreMemoryBlock(context.Background(), state.Memory, memory.ScopeProject, state.MemoryConfig)
}

// coreMemoryBlockForOpts is coreMemoryBlockForState's counterpart for the
// subagent path (plan 77, E2/E5): opts.Memory is nil for every non-chat
// caller (workflow/background paths), which coreMemoryBlock already
// degrades safely to "".
func coreMemoryBlockForOpts(opts SessionDispatcherOpts) string {
	return coreMemoryBlock(context.Background(), opts.Memory, memory.ScopeProject, opts.MemoryConfig)
}

// memoryOf and memoryConfigOf mirror chat_repl.go's ledgerRepoOf for the
// memory store (plan 77, E2), for callers building SessionDispatcherOpts
// that don't already hold state.mu.
func memoryOf(state *agentSessionState) memory.Store {
	if state == nil {
		return nil
	}
	return state.Memory
}

func memoryConfigOf(state *agentSessionState) config.MemoryConfig {
	if state == nil {
		return config.MemoryConfig{}
	}
	return state.MemoryConfig
}
