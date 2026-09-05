package uiadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestJoiningALiveEntryDoesNotReleaseItsLease is the regression test for a
// silent context-ownership loss.
//
// getOrCreateInDirLocking builds a twin session, calls Load, and only THEN
// discovers that the id resolved to a session already live in this pool. The
// twin is thrown away down the ordinary discard path - but by then Load has
// rewritten the twin's context principal to the resolved id, and
// contextHeartbeat.release reads the LIVE principal rather than an arm-time
// snapshot. So discarding the twin issued ReleaseLease against the row the
// live session still owns: that conversation stayed on screen and kept
// accepting turns while its own RenewLease silently matched no row, and its
// lease was left clear for any other process to reclaim.
//
// Joining a live entry must write nothing to the store.
func TestJoiningALiveEntryDoesNotReleaseItsLease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	store := approvalTestStore(t)
	pool := NewSessionPool(contextBoundSession(t, res, store, "launch"), res, nil, false)
	t.Cleanup(pool.CloseAll)
	if err := contextBoundSession(t, res, store, "X").Save("X"); err != nil {
		t.Fatal(err)
	}
	live, err := pool.GetOrCreate("X")
	if err != nil {
		t.Fatal(err)
	}

	var released []*chat.Session
	orig := releaseSessionLease
	releaseSessionLease = func(_ context.Context, sess *chat.Session) {
		released = append(released, sess)
	}
	defer func() { releaseSessionLease = orig }()

	// " X " sanitizes to "X", so the build resolves onto the live entry.
	got, err := pool.GetOrCreateInDir(" X ", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != live {
		t.Fatalf("resume published a second copy %p over the live entry %p", got, live)
	}
	if len(released) != 0 {
		t.Fatalf("joining a live entry released %d lease(s); the live session still owns that row", len(released))
	}
}
