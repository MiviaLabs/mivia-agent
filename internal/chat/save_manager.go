package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// SaveManagerMetrics exposes atomic counters tracked by SaveManager.
type SaveManagerMetrics struct {
	SaveAfterTurnCount int64 `json:"save_after_turn_count"`
	SaveOnExitCount    int64 `json:"save_on_exit_count"`
	PruneCount         int64 `json:"prune_count"`
}

// SaveManager handles auto-save lifecycle independently of Session.
// It persists messages through a FileSessionStore, appending appropriate
// auto-save name prefixes and pruning old exit snapshots.
//
// SaveAfterTurn creates timestamped snapshots with a "_turn_" qualifier
// and does NOT prune — safe for mid-session progress checks.
//
// SaveOnExit creates a bare exit snapshot (no qualifier) and then prunes
// old exit auto-saves to keep at most AutoSaveKeep.
type SaveManager struct {
	store    *FileSessionStore
	model    string
	provider string

	// atomic counters
	saveAfterTurn atomic.Int64
	saveOnExit    atomic.Int64
	pruneCount    atomic.Int64
}

// NewSaveManager creates a SaveManager that saves via the given store.
func NewSaveManager(store *FileSessionStore, model, provider string) *SaveManager {
	return &SaveManager{
		store:    store,
		model:    model,
		provider: provider,
	}
}

// SaveAfterTurn saves the messages as a per-turn snapshot with a "_turn_"
// qualifier in the name. Does NOT prune old auto-saves — that is deferred
// to SaveOnExit (graceful shutdown).
//
// If msgs has no meaningful content (only a system prompt or empty), this
// is a no-op.
func (m *SaveManager) SaveAfterTurn(msgs []provider.Message) error {
	if !hasContent(msgs) {
		return nil
	}
	name := uniqAutoSaveName(m.store.Dir(), "_turn_")
	if err := m.store.Save(name, msgs); err != nil {
		return err
	}
	m.saveAfterTurn.Add(1)
	return nil
}

// SaveOnExit saves messages as an exit auto-save (bare __last__ prefix)
// and then prunes old exit auto-saves to keep at most AutoSaveKeep.
//
// If msgs has no meaningful content, this is a no-op.
func (m *SaveManager) SaveOnExit(msgs []provider.Message) error {
	if !hasContent(msgs) {
		return nil
	}
	name := uniqAutoSaveName(m.store.Dir(), "")
	if err := m.store.Save(name, msgs); err != nil {
		return err
	}
	m.saveOnExit.Add(1)
	m.prune()
	return nil
}

// Metrics returns a snapshot of the tracked counters.
func (m *SaveManager) Metrics() SaveManagerMetrics {
	return SaveManagerMetrics{
		SaveAfterTurnCount: m.saveAfterTurn.Load(),
		SaveOnExitCount:    m.saveOnExit.Load(),
		PruneCount:         m.pruneCount.Load(),
	}
}

// prune removes the oldest exit auto-saves beyond AutoSaveKeep.
func (m *SaveManager) prune() {
	infos, err := m.store.List()
	if err != nil {
		return
	}
	var autoInfos []SessionInfo
	for _, si := range infos {
		if IsAutoSaveName(si.Name) {
			autoInfos = append(autoInfos, si)
		}
	}
	// List returns most-recent first; tail is oldest.
	if len(autoInfos) <= AutoSaveKeep {
		return
	}
	toDelete := autoInfos[AutoSaveKeep:]
	for _, si := range toDelete {
		if err := m.store.Delete(si.Name); err == nil {
			m.pruneCount.Add(1)
		}
	}
}

// uniqAutoSaveName generates a unique session directory name under dir.
// The suffix is inserted between AutoSaveName and the timestamp,
// e.g. "__last__turn_20250101T120000.000" when suffix is "_turn_".
// If the bare timestamp-path already exists, a numeric suffix (-1, -2, …)
// is appended to avoid collision.
func uniqAutoSaveName(dir, suffix string) string {
	base := AutoSaveName + suffix + time.Now().Format(autoSaveTimeFormat)
	name := base
	for i := 0; i < 1000; i++ {
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
	}
	return name
}

// hasContent reports whether msgs contains more than just a system prompt.
func hasContent(msgs []provider.Message) bool {
	if len(msgs) == 0 {
		return false
	}
	if len(msgs) == 1 {
		return msgs[0].Role != provider.RoleSystem
	}
	return true
}

// Dir returns the root directory of the FileSessionStore.
// Needed by SaveManager to generate unique names in the same tree.
func (fs *FileSessionStore) Dir() string {
	return fs.dir
}
