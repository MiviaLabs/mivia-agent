package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// persistFileLocks holds one *sync.Mutex per absolute file path currently
// under a locked read-modify-write update, shared by every config/agent-file
// mutator in this package (UpdateGeneralConfig, UpdateProviderConfig,
// UpdateProviderDefaultModel, UpdateChatNoticeConfig, WriteAgentFile, ...).
// Every one of these reads the whole file, mutates an in-memory value, and
// writes the whole file back - a read-modify-write with no synchronization
// of its own. Two concurrent callers targeting the SAME path (two
// SettingsStore instances in one process, a TUI edit racing a background
// goroutine, a test spawning parallel writers) can each read the same
// original content, then race to write: the last writer wins with a
// version that never saw the other edit, silently dropping it. Locking the
// full read-compute-write span per path serializes them, so the second
// edit always reads the first edit's result rather than stale content -
// the same pattern internal/tools/edit_lock.go already applies to
// search_replace/multi_edit for the identical reason, and
// internal/chat/persistence.go's sessionIOLocks applies to session files.
var persistFileLocks sync.Map // map[string]*sync.Mutex

// lockPersistFile acquires the per-path lock for path (resolved to an
// absolute path so "mivia.toml" and "./mivia.toml" from two different
// working directories are recognized as the same file) and returns a
// function that releases it.
func lockPersistFile(path string) func() {
	key := path
	if abs, err := filepath.Abs(path); err == nil {
		key = abs
	}
	v, _ := persistFileLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// writeFileAtomic writes data to path via a temp file in the same
// directory followed by os.Rename. The rename is atomic on every platform
// Go supports for same-filesystem renames, so a concurrent reader of path
// never observes a partially-written file, and a crash mid-write leaves
// the original file untouched rather than truncated - mirroring
// miviaauth.Save's own temp+rename discipline (internal/miviaauth/store.go).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".mivia-tmp-*")
	if err != nil {
		return fmt.Errorf("create a temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install the file: %w", err)
	}
	return nil
}

// updateConfigFile performs one locked, atomic read-mutate-write cycle on
// path's TOML content: acquire path's lock, decode the current file (or
// start from an empty map when it does not exist yet), let mutate edit the
// map in place, then write it back atomically via writeFileAtomic. Every
// exported config mutator in this package is a thin wrapper around this
// function so they all share the same lock (keyed by path, not by which
// mutator is called - a general-settings edit and a provider edit racing
// on the SAME file still serialize against each other) and the same
// atomic-write guarantee.
func updateConfigFile(path string, mutate func(map[string]any) error) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	unlock := lockPersistFile(path)
	defer unlock()

	raw, err := readConfigMap(path)
	if err != nil {
		return err
	}
	if err := mutate(raw); err != nil {
		return err
	}
	return writeConfigMapAtomic(path, raw)
}

// writeConfigMapAtomic marshals raw as TOML and writes it to path via
// writeFileAtomic, creating path's parent directory first if needed.
// Callers that need locking (every mutator except tests exercising the
// write path directly) go through updateConfigFile instead of calling
// this directly.
func writeConfigMapAtomic(path string, raw map[string]any) error {
	out, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal updated config: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
	}
	return writeFileAtomic(path, out, 0o600)
}
