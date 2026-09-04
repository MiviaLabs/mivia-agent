package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// resolveMemoryConfig resolves [memory] with defaults and bounds. Markdown
// memory has no project StorePath: its files live in the workspace and its
// derived index lives in the global context store. StorePath remains only for
// explicit legacy SQLite configurations.
//
// A workspace config is repo-controlled: any repository can ship its own
// .mivia/mivia.toml, so it must not name the org store its agents write into
// (plan 68, security disposition). org_id therefore comes from the user config
// file (~/.mivia/mivia.toml) unless the selected config IS that file.
func resolveMemoryConfig(file File, selectedPath string, root string, projectConfigFound bool) (MemoryConfig, error) {
	mc := file.Memory
	backend := strings.ToLower(strings.TrimSpace(mc.StoreBackend))
	if backend == "" {
		backend = DefaultMemoryConfig.StoreBackend
	}
	if strings.TrimSpace(mc.StorePath) == "" && backend != memory.BackendMarkdown {
		if projectConfigFound {
			mc.StorePath = workspace.MemoryDBPath(root)
		} else {
			mc.StorePath = TempStorePath(root, "memory")
		}
	}
	if !mc.IsEnabled() {
		mc.Enabled = boolPtr(false)
		return mc, nil
	}
	mc.Enabled = boolPtr(true)
	if backend != memory.BackendMemory && backend != memory.BackendSQLite && backend != memory.BackendMarkdown {
		return MemoryConfig{}, fmt.Errorf("[memory] store_backend must be \"memory\", \"sqlite\", or \"markdown\", got %q", mc.StoreBackend)
	}
	mc.StoreBackend = backend
	if mc.MaxEntryBytes <= 0 {
		mc.MaxEntryBytes = DefaultMemoryConfig.MaxEntryBytes
	}
	if mc.MaxEntryBytes < MinMemoryEntryBytes || mc.MaxEntryBytes > MaxMemoryEntryBytes {
		return MemoryConfig{}, fmt.Errorf("[memory] max_entry_bytes must be between %d and %d, got %d", MinMemoryEntryBytes, MaxMemoryEntryBytes, mc.MaxEntryBytes)
	}
	if mc.MaxEntries <= 0 {
		mc.MaxEntries = DefaultMemoryConfig.MaxEntries
	}
	if mc.MaxSearchResults <= 0 {
		mc.MaxSearchResults = DefaultMemoryConfig.MaxSearchResults
	}
	if mc.MaxSearchResults > MaxMemorySearchResults {
		return MemoryConfig{}, fmt.Errorf("[memory] max_search_results must be at most %d, got %d", MaxMemorySearchResults, mc.MaxSearchResults)
	}
	for _, pattern := range mc.BlockPatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return MemoryConfig{}, fmt.Errorf("[memory] block_patterns: %w", err)
		}
	}
	orgID := ""
	switch userPath := UserConfigPath(); {
	case userPath == "":
	case selectedPath != "" && filepath.Clean(selectedPath) == filepath.Clean(userPath):
		orgID = mc.OrgID // the selected config IS the user config
	default:
		orgID = readUserMemoryOrgID(userPath)
	}
	if strings.TrimSpace(orgID) != "" {
		norm, err := memory.NormalizeOrgID(orgID)
		if err != nil {
			return MemoryConfig{}, fmt.Errorf("[memory] org_id: %w", err)
		}
		mc.OrgID = norm
	} else {
		mc.OrgID = ""
	}
	return mc, nil
}

// readUserMemoryOrgID reads [memory] org_id from the user config file. A
// missing or unreadable file yields "" (org scope is simply unavailable); a
// malformed user file does not break the selected workspace config.
func readUserMemoryOrgID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var u struct {
		Memory MemoryConfig `toml:"memory"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).Decode(&u); err != nil {
		return ""
	}
	return strings.TrimSpace(u.Memory.OrgID)
}
