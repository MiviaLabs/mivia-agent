package clichat

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// The openMemoryStore chain is duplicated from internal/cli/memory_store.go
// for the memory wiring tests that moved into this package.

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
