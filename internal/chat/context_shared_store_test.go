package chat

import (
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	_ "modernc.org/sqlite"
)

// openSharedContextStore returns the durable store plus a second read-only
// handle on the same file, so the assertions can inspect the SQLite tables the
// store owns without widening its production API.
func openSharedContextStore(t *testing.T) (*storage.SQLite, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shared.db")
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store, db
}

// echoingCompleter answers with the question, so two sessions asking the same
// thing produce byte-identical user AND assistant content - the shape a user
// retyping the same message into a second `mivia chat` run produces.
type echoingCompleter struct{}

func (echoingCompleter) Name() string { return "echoing" }
func (c echoingCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	response, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}
func (c echoingCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (echoingCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &provider.Response{Content: "reply to " + latestUserMessage(req.Messages), FinishReason: "stop"}, nil
}

func newSharedStoreSession(t *testing.T, store contextstate.Store) (*Session, contextstate.Principal) {
	t.Helper()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, echoingCompleter{})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	return session, principal
}

type durableCounts struct {
	revision   contextstate.Revision
	events     int
	checkpoint int
	commits    int
}

func readDurableCounts(t *testing.T, db *sql.DB, store contextstate.Store, principal contextstate.Principal) durableCounts {
	t.Helper()
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("load durable snapshot: %v", err)
	}
	counts := durableCounts{revision: snapshot.Revision, events: len(snapshot.Source)}
	if err := db.QueryRow(`SELECT count(*) FROM context_checkpoints WHERE session_id=?`, principal.SessionID).Scan(&counts.checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM context_operations WHERE session_id=? AND kind='commit'`, principal.SessionID).Scan(&counts.commits); err != nil {
		t.Fatal(err)
	}
	return counts
}

// TestIntegrationSecondSessionInSharedStorePersistsRepeatedTurns is the live
// production regression: `mivia chat` reported `checkpoint conflict` and
// stopped persisting history once a second run in the same workspace produced
// content an earlier run had already stored.
//
// It runs the reported journey twice against ONE store - clear, two agent
// turns, then a third ordinary send - and asserts on both halves of a turn:
// the in-memory conversation keeps every prior turn, and context_sessions,
// context_checkpoints, context_source_events and context_operations all
// advance together.
func TestIntegrationSecondSessionInSharedStorePersistsRepeatedTurns(t *testing.T) {
	store, db := openSharedContextStore(t)

	questions := []string{"first question", "second question", "third question"}
	runJourney := func(t *testing.T, label string) {
		t.Helper()
		session, principal := newSharedStoreSession(t, store)
		_ = session.Clear()
		for index, question := range questions {
			if _, err := session.SendUser(context.Background(), question, io.Discard); err != nil {
				t.Fatalf("%s turn %d (%q): %v", label, index+1, question, err)
			}
			counts := readDurableCounts(t, db, store, principal)
			wantEvents := 2 * (index + 1)
			// Clear advances the head once before any turn, so durable and
			// session revisions are turn count + 1.
			want := durableCounts{
				revision:   contextstate.Revision{Session: uint64(index + 2), Durable: uint64(index + 2), Source: uint64(wantEvents)},
				events:     wantEvents,
				checkpoint: index + 1,
				commits:    index + 1,
			}
			if counts != want {
				t.Fatalf("%s turn %d durable state = %+v, want %+v", label, index+1, counts, want)
			}
			if got := len(session.MessagesCopy()); got != 1+wantEvents {
				t.Fatalf("%s turn %d in-memory history = %d messages, want %d", label, index+1, got, 1+wantEvents)
			}
		}
		history := session.MessagesCopy()
		for index, question := range questions {
			if got := history[1+2*index].Content; got != question {
				t.Fatalf("%s history lost turn %d: message = %q, want %q", label, index+1, got, question)
			}
		}
	}

	runJourney(t, "first session")
	runJourney(t, "second session")
}

// TestIntegrationOneSessionRepeatingItselfStillCommits keeps the property the
// global content key did provide, through the real commit path rather than the
// minting function: one session sending the identical message twice resolves to
// one payload row and both turns still publish.
func TestIntegrationOneSessionRepeatingItselfStillCommits(t *testing.T) {
	store, db := openSharedContextStore(t)
	session, principal := newSharedStoreSession(t, store)
	for turn := 1; turn <= 2; turn++ {
		if _, err := session.SendUser(context.Background(), "same message", io.Discard); err != nil {
			t.Fatalf("turn %d repeating identical content: %v", turn, err)
		}
	}
	counts := readDurableCounts(t, db, store, principal)
	want := durableCounts{
		revision: contextstate.Revision{Session: 2, Durable: 2, Source: 4},
		events:   4, checkpoint: 2, commits: 2,
	}
	if counts != want {
		t.Fatalf("durable state = %+v, want %+v", counts, want)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM context_payloads WHERE session_id=? AND size=?`, principal.SessionID, len("same message")).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("repeated content stored %d payload rows, want one deduplicated row", rows)
	}
}

// TestIntegrationSharedStoreSessionsKeepSeparatePayloadRows pins that the two
// runs above stayed principal-scoped rather than sharing one payload row: a
// row owned by another session would satisfy neither ReadPayload's owner gate
// nor the deletion accounting that keys on it.
func TestIntegrationSharedStoreSessionsKeepSeparatePayloadRows(t *testing.T) {
	store, db := openSharedContextStore(t)

	var sessions []string
	for range 2 {
		session, principal := newSharedStoreSession(t, store)
		if _, err := session.SendUser(context.Background(), "identical question", io.Discard); err != nil {
			t.Fatalf("session %s: %v", principal.SessionID, err)
		}
		sessions = append(sessions, principal.SessionID)
	}
	for _, sessionID := range sessions {
		var owned int
		if err := db.QueryRow(`SELECT count(*) FROM context_payloads WHERE session_id=?`, sessionID).Scan(&owned); err != nil {
			t.Fatal(err)
		}
		if owned == 0 {
			t.Fatalf("session %s owns no payload rows", sessionID)
		}
	}
	var orphaned int
	if err := db.QueryRow(`SELECT count(*) FROM context_source_events e JOIN context_payloads p ON p.ref=e.payload_ref WHERE p.session_id <> e.session_id`).Scan(&orphaned); err != nil {
		t.Fatal(err)
	}
	if orphaned != 0 {
		t.Fatalf("%d source events reference another session's payload row", orphaned)
	}
}
