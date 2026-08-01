package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
// SaveAfterTurn overwrites a single rolling snapshot named with a "_turn_"
// qualifier and does NOT prune - safe for mid-session progress checks.
//
// SaveOnExit creates a bare exit snapshot (no qualifier) and then prunes
// old auto-saves back to their retention budgets.
type SaveManager struct {
	store        *FileSessionStore
	model        string
	providerName string

	// turnMu guards turnSaveName.
	turnMu sync.Mutex
	// turnSaveName is the rolling directory SaveAfterTurn overwrites, minted
	// lazily on the first turn. Minting a fresh name per turn (as this did)
	// left one full transcript copy on disk per turn, so a long session
	// accumulated hundreds of directories that List had to stat and parse on
	// every render. The name stays per-manager rather than a fixed constant so
	// two mivia processes in one workspace cannot overwrite each other's
	// crash-recovery snapshot.
	turnSaveName string

	// atomic counters
	saveAfterTurn atomic.Int64
	saveOnExit    atomic.Int64
	pruneCount    atomic.Int64
}

// NewSaveManager creates a SaveManager that saves via the given store.
func NewSaveManager(store *FileSessionStore, model, providerName string) *SaveManager {
	return &SaveManager{
		store:        store,
		model:        model,
		providerName: providerName,
	}
}

// SaveAfterTurn overwrites this manager's rolling per-turn snapshot, named
// with a "_turn_" qualifier. Does NOT prune old auto-saves - that is deferred
// to SaveOnExit (graceful shutdown).
//
// Each save rewrites the whole transcript, so the single directory always
// holds the newest state: the crash-recovery guarantee is unchanged while
// disk usage stays flat across the session.
//
// If msgs has no meaningful content (only a system prompt or empty), this
// is a no-op.
func (m *SaveManager) SaveAfterTurn(msgs []provider.Message) error {
	return m.SaveAfterTurnWithModel(msgs, m.model)
}

// SaveAfterTurnWithModel persists a transcript with the model selected when
// the caller captured that transcript.
func (m *SaveManager) SaveAfterTurnWithModel(msgs []provider.Message, model string) error {
	return m.SaveAfterTurnWithSelection(msgs, m.providerName, model)
}

// SaveAfterTurnWithSelection persists a transcript with a matching binding.
func (m *SaveManager) SaveAfterTurnWithSelection(msgs []provider.Message, providerName, model string) error {
	if !hasContent(msgs) {
		return nil
	}
	if providerName == "" {
		providerName = m.providerName
	}
	if model == "" {
		model = m.model
	}
	if err := m.store.Save(m.turnSnapshotName(), msgs, model, providerName); err != nil {
		return err
	}
	m.saveAfterTurn.Add(1)
	return nil
}

// turnSnapshotName returns this manager's rolling snapshot name, minting it
// on first use.
func (m *SaveManager) turnSnapshotName() string {
	m.turnMu.Lock()
	defer m.turnMu.Unlock()
	if m.turnSaveName == "" {
		m.turnSaveName = uniqAutoSaveName(m.store.Dir(), turnSaveMarker)
	}
	return m.turnSaveName
}

// SaveOnExit saves messages as an exit auto-save (bare __last__ prefix)
// and then prunes old exit auto-saves to keep at most AutoSaveKeep.
//
// If msgs has no meaningful content, this is a no-op.
func (m *SaveManager) SaveOnExit(msgs []provider.Message) error {
	return m.SaveOnExitWithModel(msgs, m.model)
}

// SaveOnExitWithModel persists an exit snapshot with the model selected when
// the caller captured that transcript.
func (m *SaveManager) SaveOnExitWithModel(msgs []provider.Message, model string) error {
	return m.SaveOnExitWithSelection(msgs, m.providerName, model)
}

// SaveOnExitWithSelection persists an exit snapshot with a matching binding.
func (m *SaveManager) SaveOnExitWithSelection(msgs []provider.Message, providerName, model string) error {
	if !hasContent(msgs) {
		return nil
	}
	if providerName == "" {
		providerName = m.providerName
	}
	if model == "" {
		model = m.model
	}
	name := uniqAutoSaveName(m.store.Dir(), "")
	if err := m.store.Save(name, msgs, model, providerName); err != nil {
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

// prune reclaims auto-saves beyond their retention budgets. It first rebuilds
// metadata for interrupted saves: the shipped CLI always wires a SaveManager,
// so this is the only path where crash leftovers are ever recovered.
func (m *SaveManager) prune() {
	cleanupOrphanedSessions(m.store.Dir())

	infos, err := m.store.List()
	if err != nil {
		return
	}
	// Read the rolling name without minting one: a session that never took a
	// turn snapshot must not leave an empty directory behind on exit.
	m.turnMu.Lock()
	live := m.turnSaveName
	m.turnMu.Unlock()

	for _, si := range expiredAutoSaves(infos, live) {
		if err := m.store.Delete(si.Name); err == nil {
			m.pruneCount.Add(1)
		}
	}
}

// autoSaveSeq is incremented on each uniqAutoSaveName call as a
// tiebreaker for concurrent saves at the same nanosecond.
var autoSaveSeq atomic.Int64

// uniqAutoSaveName generates a unique session directory name under dir.
// The suffix is inserted between AutoSaveName and the timestamp,
// e.g. "__last__turn_20250101T120000.000" when suffix is "_turn_".
// Uses os.Mkdir as an atomic probe instead of os.Stat to eliminate the
// TOCTOU race between check-and-create. Two concurrent callers cannot
// both claim the same name.
func uniqAutoSaveName(dir, suffix string) string {
	base := AutoSaveName + suffix + time.Now().Format(autoSaveTimeFormat)
	name := base
	for i := 0; i < 1000; i++ {
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		candidate := filepath.Join(dir, name)
		err := os.Mkdir(candidate, 0o755)
		if err == nil {
			// Successfully created the directory atomically - we own this name.
			// Leave the empty dir in place; Save() recreates via MkdirAll
			// (no-op on existing dir) before writing chunk files.
			return name
		}
		if !os.IsExist(err) {
			// Permission error or other - stop retrying.
			break
		}
		// os.IsExist: directory already exists, try next suffix.
	}
	// All 1000 names exist - extremely unlikely. Fall back to nanosecond precision.
	return fmt.Sprintf("%s-%d-%d", base, time.Now().UnixNano(), autoSaveSeq.Add(1))
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
