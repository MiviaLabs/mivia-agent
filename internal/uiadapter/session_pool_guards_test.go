package uiadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The guards this file covers are the cheap early returns inside
// attachSyncLocked, ReattachSyncAfterLogin and ReleaseLeases: a session with
// no id, nil pool entries, and alias duplicates of one session under several
// ids. Each is one `continue` away from a nil-map-index panic that would take
// down the whole TUI pool, so they are pinned directly. HOME is emptied for
// every test here: with no token path there is no logged-in session, so
// attachSyncLocked's Sync.Active check returns before chatsync.OpenSession
// could attempt a network call, and the tests stay hermetic.

func TestSessionPool_AttachSyncLocked_EmptySessionIDReturnsEarly(t *testing.T) {
	t.Setenv("HOME", "")
	pool := &SessionPool{
		sessions:     make(map[string]*chat.Session),
		convs:        make(map[string]*Conversation),
		syncSessions: make(map[string]*chatsync.SyncSession),
		busReleases:  make(map[string]func()),
		res:          &config.Resolved{Model: "m"},
	}
	pool.attachSyncLocked(&chat.Session{SessionID: "", EventBus: events.New()})
	if len(pool.syncSessions) != 0 {
		t.Fatalf("a session with no id must not attach, got syncSessions %v", pool.syncSessions)
	}
	if len(pool.busReleases) != 0 {
		t.Fatalf("a session with no id must not bind a bus release, got %v", pool.busReleases)
	}
}

// TestSessionPool_ReattachAndReleaseTolerateNilAndAliasEntries drives
// ReattachSyncAfterLogin's snapshot loop and ReleaseLeases over a pool that
// carries every degenerate entry at once: a nil session, one session
// registered under two alias ids, a session with no id, and a nil sync-session
// entry. None may panic; nothing may attach (logged out); ReleaseLeases
// releases each DISTINCT session exactly once.
func TestSessionPool_ReattachAndReleaseTolerateNilAndAliasEntries(t *testing.T) {
	t.Setenv("HOME", "")
	shared := &chat.Session{SessionID: "sess-shared", EventBus: events.New()}
	pool := &SessionPool{
		sessions: map[string]*chat.Session{
			"":        {EventBus: events.New()}, // no id: attachSyncLocked's id guard
			"alias-a": shared,                   // duplicate: the seen-map continue
			"alias-b": shared,
			"ghost":   nil, // nil: the nil continue
		},
		convs:        make(map[string]*Conversation),
		syncSessions: map[string]*chatsync.SyncSession{"gone": nil}, // nil sync entry: ReleaseLeases's continue
		busReleases:  make(map[string]func()),
		res:          &config.Resolved{Model: "m"},
	}

	pool.ReattachSyncAfterLogin()
	// Only the pre-seeded nil entry may remain: Reattach never attaches while
	// logged out, and it never clears existing entries (ReleaseLeases does).
	if len(pool.syncSessions) != 1 || pool.syncSessions["gone"] != nil {
		t.Fatalf("no session may attach while logged out; only the pre-seeded nil entry may remain, got %v", pool.syncSessions)
	}

	released := 0
	prevLease := releaseSessionLease
	releaseSessionLease = func(context.Context, *chat.Session) { released++ }
	t.Cleanup(func() { releaseSessionLease = prevLease })

	pool.ReleaseLeases(context.Background())
	// Distinct non-nil sessions only: the ""-id session and shared. The ghost
	// is skipped, the aliases collapse to one release, the nil sync entry is
	// skipped.
	if released != 2 {
		t.Fatalf("ReleaseLeases released %d distinct sessions, want 2", released)
	}
	if len(pool.syncSessions) != 0 || len(pool.busReleases) != 0 {
		t.Fatalf("ReleaseLeases must drain syncSessions and busReleases, got %v / %v",
			pool.syncSessions, pool.busReleases)
	}
}
