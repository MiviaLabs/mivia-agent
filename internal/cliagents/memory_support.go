package cliagents

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// OpenMemoryStoreWithReadOnly resolves the [memory] config to a memory store.
// readOnly controls memory.Config.ReadOnly. Exported so internal/cli's
// openMemoryStore and openMemoryStoreReadOnly can delegate here without
// duplicating the path-resolution logic.
func OpenMemoryStoreWithReadOnly(root string, mc config.MemoryConfig, readOnly bool) (memory.Store, error) {
	if strings.ToLower(strings.TrimSpace(mc.StoreBackend)) == memory.BackendMarkdown {
		return openMarkdownMemoryStore(root, mc, readOnly)
	}
	projectPath := strings.TrimSpace(mc.StorePath)
	if projectPath == "" {
		// This branch is now only reachable when the caller constructs
		// config.MemoryConfig{} directly, bypassing config.Load()/
		// resolveMemoryConfig - every config.Load()-sourced MemoryConfig now
		// always has StorePath filled by resolveMemoryConfig's new
		// three-tier default. Confirmed sole real caller that still needs
		// it: internal/cliagents/memory_support_coverage_test.go:34-38
		// (TestOpenMemoryStoreRejectsMissingPath).
		projectPath = workspace.MemoryDBPath(root)
	} else {
		projectPath = config.ExpandPath(projectPath)
		// Clean before every use: an operator-supplied path may carry dot-dot
		// or double-slash segments, and an unclean spelling that names the
		// temp store must not slip past the hardening gate below.
		projectPath = filepath.Clean(projectPath)
		if !filepath.IsAbs(projectPath) {
			if projectPath == ".." || strings.HasPrefix(projectPath, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("memory store_path %q escapes the workspace root", mc.StorePath)
			}
			projectPath = filepath.Join(root, projectPath)
		}
	}
	hardenTempStore := SameFilePath(runtime.GOOS, projectPath, config.TempStorePath(root, "memory"))
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
		HardenTempStore:  hardenTempStore,
	}
	return memory.Open(cfg)
}

func openMarkdownMemoryStore(root string, mc config.MemoryConfig, readOnly bool) (memory.Store, error) {
	projectRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("memory project root: %w", err)
	}
	orgDir := workspace.GlobalMemoryDir()
	if orgDir == "" && strings.TrimSpace(mc.OrgID) != "" {
		return nil, errors.New("memory organization directory is unavailable")
	}
	source, err := memory.NewMarkdownSource(projectRoot, orgDir, mc.OrgID)
	if err != nil {
		return nil, fmt.Errorf("memory Markdown source: %w", err)
	}
	indexPath := workspace.GlobalContextStorePath(projectRoot)
	var index *storage.SQLite
	if readOnly {
		index, err = storage.OpenSQLiteReadOnly(indexPath)
	} else {
		index, err = storage.OpenSQLiteWithOptions(indexPath, storage.Options{Harden: true})
	}
	if err != nil {
		return nil, fmt.Errorf("memory index: %w", err)
	}
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{
		Source: source, Index: index, ProjectID: projectRoot, OrgID: mc.OrgID,
		MaxSearchResults: mc.MaxSearchResults,
		Limits:           memory.Limits{MaxEntryBytes: mc.MaxEntryBytes, BlockPatterns: mc.BlockPatterns},
		ReadOnly:         readOnly,
	})
	if err != nil {
		_ = index.Close()
		return nil, err
	}
	return &ownedMarkdownStore{Store: store, index: index}, nil
}

type ownedMarkdownStore struct {
	memory.Store
	index *storage.SQLite
}

func (s *ownedMarkdownStore) Close() error {
	storeErr := s.Store.Close()
	indexErr := s.index.Close()
	if storeErr != nil {
		return storeErr
	}
	return indexErr
}

// SameFilePath reports whether two path spellings name the same file:
// both are normalized (backslashes go to slashes, dot-dot and double-slash
// segments resolve away), then compared - case-folded on Windows, whose
// filesystems match case-insensitively, and byte-exact elsewhere. goos is
// a parameter, not runtime.GOOS, so the Windows branch stays testable from
// every OS. Normalization is done by hand because filepath.Clean is
// host-dependent (it treats backslashes as plain bytes on Unix), which a
// GOOS-parameterized comparison cannot lean on. Accepted limits, each
// benign in direction (a miss only skips the chmod of the file the store is
// opening anyway, never hardens a wrong file): a literal backslash inside a
// Unix filename reads as a separator, and a symlink or 8.3 short-name alias
// of the temp path compares unequal - both require deliberately spelling
// out our hash-named TempStorePath, which the default-filled path never
// does. Resolving them needs live-filesystem calls, which would give back
// the host-dependence this pure seam exists to remove. This is
// deliberately NOT config.sameFilePath (internal/config/agents_io.go),
// which resolves symlinks on the live filesystem; this one is pure, so a
// path that does not exist yet still compares. Empty paths never match.
// SameFilePath is the shared gate helper for ad-hoc store hardening: the
// clichat and cliworkflow orchestration-ledger gates call this exported
// form so all three packages compare paths by one contract.
func SameFilePath(goos, a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = normalizeFilePath(a), normalizeFilePath(b)
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// normalizeFilePath is SameFilePath's host-independent path cleaner.
func normalizeFilePath(p string) string {
	return path.Clean(strings.ReplaceAll(p, `\`, "/"))
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
