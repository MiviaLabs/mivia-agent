package uiadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestSessionPoolReleaseLeasesVisitsEveryDistinctSession is the regression
// test for the pooled-lease leak: only the primary startup session was ever
// released on shutdown, so any OTHER session resumed in the TUI kept its
// context lease fresh and the next process's resume was refused for up to
// the full lease TTL. ReleaseLeases must visit every distinct pooled
// session exactly once - including sessions registered under two ids (raw
// and canonical), which must not be released twice.
func TestSessionPoolReleaseLeasesVisitsEveryDistinctSession(t *testing.T) {
	primary := chat.NewSession(&config.Resolved{}, nil)
	resumed := chat.NewSession(&config.Resolved{}, nil)

	pool := NewSessionPool(primary, nil, nil, false)
	pool.mu.Lock()
	pool.sessions["resumed-id"] = resumed
	// The same session under its second (canonical) id, as GetOrCreate
	// registers when the loaded session's own id differs from the requested
	// one.
	pool.sessions["resumed-alias"] = resumed
	pool.mu.Unlock()

	visited := map[*chat.Session]int{}
	orig := releaseSessionLease
	releaseSessionLease = func(_ context.Context, sess *chat.Session) {
		visited[sess]++
	}
	defer func() { releaseSessionLease = orig }()

	pool.ReleaseLeases(context.Background())

	if len(visited) != 2 {
		t.Fatalf("visited %d distinct sessions, want 2 (primary + resumed): %v", len(visited), visited)
	}
	if visited[primary] != 1 {
		t.Fatalf("primary session released %d times, want 1", visited[primary])
	}
	if visited[resumed] != 1 {
		t.Fatalf("aliased session released %d times, want exactly 1", visited[resumed])
	}
}
