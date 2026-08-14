package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// busyThenOkCatalog implements contextstate.SessionCatalog and
// contextstate.SessionFirstMessageSource, failing FirstUserMessage with a
// SQLITE_BUSY-shaped error a fixed number of times before returning opener.
// Mirrors the desktop app's own real-world shape: mivia's chat sidecar keeps
// a long-lived writer connection open on the same context.db a concurrent
// `sessions list` one-shot process reads from, so a transient SQLITE_BUSY on
// that read is expected, routine contention - not a real failure - and
// should never be indistinguishable from "no checkpoint yet".
type busyThenOkCatalog struct {
	failuresLeft int
	opener       string
	calls        int
}

func (c *busyThenOkCatalog) FirstUserMessage(context.Context, contextstate.Principal, string) (string, error) {
	c.calls++
	if c.failuresLeft > 0 {
		c.failuresLeft--
		return "", errors.New("sqlite: SQLITE_BUSY: database is locked")
	}
	return c.opener, nil
}

func (c *busyThenOkCatalog) SaveSession(context.Context, contextstate.Principal, string, []byte, string, string, int, int, int, contextstate.SessionSaveOptions) error {
	return nil
}

func (c *busyThenOkCatalog) LoadSession(context.Context, contextstate.Principal, string) ([]byte, contextstate.SessionCatalogInfo, error) {
	return nil, contextstate.SessionCatalogInfo{}, nil
}

func (c *busyThenOkCatalog) ListSessions(context.Context, contextstate.Principal) ([]contextstate.SessionCatalogInfo, error) {
	return nil, nil
}

func (c *busyThenOkCatalog) DeleteSessionSnapshot(context.Context, contextstate.Principal, string) error {
	return nil
}

func (c *busyThenOkCatalog) PruneSessionSnapshots(context.Context, contextstate.Principal, []string) error {
	return nil
}

func testPrincipal(t *testing.T) contextstate.Principal {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "sess", "subject")
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

// TestFillSessionTitlesRetriesOnTransientBusy pins the fix for raw
// session-id titles in the desktop app's sidebar: a SQLITE_BUSY on the
// first-message lookup (routine contention with the desktop app's own
// long-lived chat sidecar writer) must be retried, not silently treated the
// same as "no checkpoint yet" - which previously left the session titled by
// its raw id forever, even once the lock cleared.
func TestFillSessionTitlesRetriesOnTransientBusy(t *testing.T) {
	catalog := &busyThenOkCatalog{failuresLeft: 2, opener: "opener after busy retries"}
	out := []SessionInfo{{SessionID: "sess"}}

	fillSessionTitles(context.Background(), catalog, testPrincipal(t), out)

	if out[0].Title != "opener after busy retries" {
		t.Fatalf("Title = %q, want the opener filled in after retrying past SQLITE_BUSY", out[0].Title)
	}
	if catalog.calls != 3 {
		t.Fatalf("FirstUserMessage calls = %d, want 3 (1 initial + 2 busy retries)", catalog.calls)
	}
}

// TestFillSessionTitlesGivesUpAfterExhaustingRetries pins the ceiling: a
// SQLITE_BUSY that never clears must not retry forever or block the caller
// indefinitely - it gives up after a bounded number of attempts and leaves
// the title unset, same as today's "no checkpoint yet" behavior.
func TestFillSessionTitlesGivesUpAfterExhaustingRetries(t *testing.T) {
	catalog := &busyThenOkCatalog{failuresLeft: 99, opener: "unreachable"}
	out := []SessionInfo{{SessionID: "sess"}}

	fillSessionTitles(context.Background(), catalog, testPrincipal(t), out)

	if out[0].Title != "" {
		t.Fatalf("Title = %q, want empty after exhausting retries", out[0].Title)
	}
	if catalog.calls != len(firstMessageBusyRetryDelays)+1 {
		t.Fatalf("FirstUserMessage calls = %d, want %d (1 initial + %d retries)",
			catalog.calls, len(firstMessageBusyRetryDelays)+1, len(firstMessageBusyRetryDelays))
	}
}

// TestFillSessionTitlesDoesNotRetryNonBusyErrors pins that only the
// transient-busy shape gets retried - any other error (a real failure) skips
// the title fill immediately, same as today, so a genuine error is not
// masked behind a few hundred ms of pointless retrying.
func TestFillSessionTitlesDoesNotRetryNonBusyErrors(t *testing.T) {
	out := []SessionInfo{{SessionID: "sess"}}
	catalog := &erroringCatalog{err: errors.New("disk I/O error")}

	fillSessionTitles(context.Background(), catalog, testPrincipal(t), out)

	if out[0].Title != "" {
		t.Fatalf("Title = %q, want empty (non-busy error must not retry or fill)", out[0].Title)
	}
	if catalog.calls != 1 {
		t.Fatalf("FirstUserMessage calls = %d, want 1 (no retry for a non-busy error)", catalog.calls)
	}
}

type erroringCatalog struct {
	busyThenOkCatalog
	err   error
	calls int
}

func (c *erroringCatalog) FirstUserMessage(context.Context, contextstate.Principal, string) (string, error) {
	c.calls++
	return "", c.err
}
