package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// validateWritePathBlocklist rejects entries that cannot protect anything:
// entries that clean to "." (empty, whitespace-only, "x/..") or absolute
// paths are silent no-ops in isWriteDeniedPath, so a misconfigured blocklist
// fails closed at load instead of failing open at write time. The same rules
// apply to removals; an entry in both keys is a contradiction.
func validateWritePathBlocklist(tc ToolsConfig) error {
	if err := validateBlocklistEntries("write_path_blocklist", tc.WritePathBlocklist); err != nil {
		return err
	}
	if err := validateBlocklistEntries("write_path_blocklist_remove", tc.WritePathBlocklistRemove); err != nil {
		return err
	}
	removed := make(map[string]bool, len(tc.WritePathBlocklistRemove))
	for _, entry := range tc.WritePathBlocklistRemove {
		removed[filepath.ToSlash(filepath.Clean(strings.TrimSpace(entry)))] = true
	}
	for _, entry := range tc.WritePathBlocklist {
		if removed[filepath.ToSlash(filepath.Clean(strings.TrimSpace(entry)))] {
			return fmt.Errorf("[tools] entry %q is in both write_path_blocklist and write_path_blocklist_remove; the keys contradict - remove it from one of them", entry)
		}
	}
	return nil
}

// validateBlocklistEntries applies the shared entry rules to one blocklist
// key: entries that cannot match any real workspace-relative path are load
// errors, never silent no-ops.
func validateBlocklistEntries(key string, entries []string) error {
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		cleaned := filepath.Clean(trimmed)
		if cleaned == "." {
			return fmt.Errorf("[tools] %s entry %q is empty or resolves to the workspace root; use a relative path", key, entry)
		}
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("[tools] %s entry %q escapes the workspace; use a relative path inside the workspace", key, entry)
		}
		if filepath.IsAbs(cleaned) {
			return fmt.Errorf("[tools] %s entry %q is absolute; use a workspace-relative path", key, entry)
		}
		// A backslash separator is a single filename character on non-Windows
		// hosts, so an entry written with Windows separators can never match
		// a real workspace-relative path there. Reject it instead of letting
		// the protection silently not exist.
		if runtime.GOOS != "windows" && strings.Contains(trimmed, "\\") {
			return fmt.Errorf("[tools] %s entry %q uses a backslash separator; use forward slashes", key, entry)
		}
	}
	return nil
}

// normalizeWritePathBlocklist normalizes both blocklist keys so the write
// tools compare exact workspace-relative paths: trim whitespace, collapse
// separators, use forward slashes. Defaults are NOT injected here; the
// workflow registry composes DefaultWritePathBlocklist (empty by default) +
// additions - removals at build time.
func normalizeWritePathBlocklist(tc ToolsConfig) ToolsConfig {
	if len(tc.WritePathBlocklist) > 0 {
		norm := make([]string, 0, len(tc.WritePathBlocklist))
		for _, entry := range tc.WritePathBlocklist {
			norm = append(norm, filepath.ToSlash(filepath.Clean(strings.TrimSpace(entry))))
		}
		tc.WritePathBlocklist = slices.Clone(norm)
	}
	if len(tc.WritePathBlocklistRemove) > 0 {
		norm := make([]string, 0, len(tc.WritePathBlocklistRemove))
		for _, entry := range tc.WritePathBlocklistRemove {
			norm = append(norm, filepath.ToSlash(filepath.Clean(strings.TrimSpace(entry))))
		}
		tc.WritePathBlocklistRemove = slices.Clone(norm)
	}
	return tc
}
