package cli

import (
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
// [memory] config.
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
	}
	return memory.Open(cfg)
}
