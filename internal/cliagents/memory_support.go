package cliagents

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

// OpenMemoryStoreWithReadOnly resolves the [memory] config to a memory store.
// readOnly controls memory.Config.ReadOnly. Exported so internal/cli's
// openMemoryStore and openMemoryStoreReadOnly can delegate here without
// duplicating the path-resolution logic.
func OpenMemoryStoreWithReadOnly(root string, mc config.MemoryConfig, readOnly bool) (memory.Store, error) {
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

// WireSessionMemory wires the memory store into the session tool options so
// memory_save and memory_search register (plan 68). A nil res or a disabled
// [memory] section leaves opts.Memory unchanged.
func WireSessionMemory(opts *tools.DefaultOptions, root string, res *config.Resolved) error {
	if res == nil || !res.Memory.IsEnabled() {
		return nil
	}
	store, err := OpenMemoryStoreWithReadOnly(root, res.Memory, false)
	if err != nil {
		return fmt.Errorf("memory store: %w", err)
	}
	opts.Memory = store
	return nil
}

// coreMemoryBlock builds the auto-injected system-prompt block. It is a true
// no-op ("") when injection is disabled or the scope has no core entries.
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

// CoreMemoryBlockForState returns the core-memory injection block scoped to
// the project for the given session state. Callers hold state.mu per the
// LedgerRepo/Memory field convention. context.Background() is used because
// this is always a session-init or turn-boundary call with no request context.
func CoreMemoryBlockForState(state *AgentSessionState) string {
	if state == nil {
		return ""
	}
	return coreMemoryBlock(context.Background(), state.Memory, memory.ScopeProject, state.MemoryConfig)
}

// CoreMemoryBlockForOpts is CoreMemoryBlockForState for the subagent path
// (plan 77, E2/E5): opts.Memory is nil for workflow/background callers,
// which coreMemoryBlock degrades safely to "".
func CoreMemoryBlockForOpts(opts SessionDispatcherOpts) string {
	return coreMemoryBlock(context.Background(), opts.Memory, memory.ScopeProject, opts.MemoryConfig)
}

// MemoryOf returns the session-lifetime memory store from state without
// requiring the caller to hold state.mu (plan 77, E2).
func MemoryOf(state *AgentSessionState) memory.Store {
	if state == nil {
		return nil
	}
	return state.Memory
}

// MemoryConfigOf returns the resolved [memory] config from state without
// requiring the caller to hold state.mu (plan 77, E2).
func MemoryConfigOf(state *AgentSessionState) config.MemoryConfig {
	if state == nil {
		return config.MemoryConfig{}
	}
	return state.MemoryConfig
}
