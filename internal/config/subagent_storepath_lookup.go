package config

import (
	"fmt"
	"os"
	"strings"
)

// LoadSubagentStorePath decodes the TOML file at path and returns the
// explicitly-set [subagents] store_path ("", false) when the file does not
// set one. Unlike Load it performs no provider resolution, so reading a
// workspace mivia.toml that only carries workspace settings (workflows,
// verifiers, MCP) never fails on a missing [providers] section:
// repositorySessionStorePath only needs this one key and must not inherit
// provider requirements - an explicit-path Load re-runs resolveProvider and
// hard-fails with "[providers.openrouter]: models must be non-empty" for a
// user whose workspace config declares no provider but whose user config
// does.
//
// The returned path is the file's raw value; it is only meaningful when set
// is true, matching Resolved.StorePathSet's contract (an unset store_path is
// defaulted by resolveSubagentStoreBackend, but callers that branch on the
// flag never consume the default).
func LoadSubagentStorePath(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" {
		return "", false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read config %s: %w", path, err)
	}
	var file File
	if err := decodeConfigInto(data, path, &file); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(file.Subagents.StorePath) == "" {
		return "", false, nil
	}
	return file.Subagents.StorePath, true, nil
}
