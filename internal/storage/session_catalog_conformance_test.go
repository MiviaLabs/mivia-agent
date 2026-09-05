package storage

// CONFORMANCE SUITE — every session-catalog namespace, one set of assertions.
//
// The catalog has two namespaces: plain sessions (instance_id IS NULL) and
// managed-worktree sessions (instance_id = <instance>). They are two
// implementations of one contract, and in the 2026-09-05 worktree batch every
// single divergence between them was a reachable bug:
//
//   - a turn-only live session (no snapshot) was loadable in the plain
//     namespace and "session not found" in the worktree one, which is what
//     made worktree sessions unresumable;
//   - the staleness rule that stops a /clear'ed conversation being resurrected
//     from an older snapshot existed only in the plain namespace;
//   - deleting a session tombstoned the live row in neither namespace
//     reliably, so a "deleted" conversation stayed loadable.
//
// Each of those was found one namespace at a time, fixed one namespace at a
// time, and the sibling stayed broken until someone looked. A per-namespace
// test answers "does this loader work?"; only a table over BOTH answers "do
// they agree?", which is the question this codebase keeps getting wrong.
//
// Two rules for anyone extending this file:
//  1. The assertions are shared and only the NAMESPACE differs - a zero
//     WorktreeInstance is the plain namespace, a set one is the worktree
//     namespace. Do not branch inside an assertion; if a behaviour is
//     genuinely namespace-specific, say why in a comment.
//  2. When you fix a catalog bug in one namespace, add the assertion HERE
//     first and watch it fail for the sibling too.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// catalogNamespace is one implementation of the catalog contract. Everything
// the assertions need is derived from instance, so a third namespace joins by
// adding a row here.
type catalogNamespace struct {
	name     string
	instance contextstate.WorktreeInstance
}

var catalogNamespaces = []catalogNamespace{
	{name: "plain"},
	{name: "worktree", instance: contextstate.WorktreeInstance{Worktree: "wt-conf", ID: "wt_00000000000000ff"}},
}

// open prepares a store with this namespace's session live and bound.
func (ns catalogNamespace) open(t *testing.T) (*SQLite, contextstate.Principal, contextstate.BindingRevision) {
	t.Helper()
	store, principal := openContextTestStore(t)
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if !ns.instance.IsZero() {
		catalog := any(store).(contextstate.WorktreeSessionCatalog)
		dir := filepath.Join(t.TempDir(), "worktrees", ns.instance.Worktree)
		if err := catalog.BeginWorktreeCreation(ctx, principal, ns.instance, dir); err != nil {
			t.Fatalf("%s: BeginWorktreeCreation: %v", ns.name, err)
		}
		if err := catalog.RegisterWorktreeInstance(ctx, principal, ns.instance, dir); err != nil {
			t.Fatalf("%s: RegisterWorktreeInstance: %v", ns.name, err)
		}
	}
	binding := contextTestBinding(t)
	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{
		Principal: principal, Binding: binding, WorktreeInstance: ns.instance,
	}); err != nil {
		t.Fatalf("%s: EnsureSession: %v", ns.name, err)
	}
	return store, principal, binding
}

func (ns catalogNamespace) commitTurn(t *testing.T, store *SQLite, p contextstate.Principal, b contextstate.BindingRevision, key, content string, expected contextstate.Revision) {
	t.Helper()
	req, err := interleaveCommitRequest(p, ns.instance, expected, b, key, content)
	if err != nil {
		t.Fatalf("%s: build commit: %v", ns.name, err)
	}
	if err := store.Commit(context.Background(), req); err != nil {
		t.Fatalf("%s: Commit: %v", ns.name, err)
	}
}

// saveSnapshot writes a projection of the live session - the shape a failed
// turn leaves behind (adoptFailedTurnSnapshot), which is the only copy that
// turn's history has.
func (ns catalogNamespace) saveSnapshot(t *testing.T, store *SQLite, p contextstate.Principal, b contextstate.BindingRevision, payload string, revision uint64) {
	t.Helper()
	if err := store.SaveSession(context.Background(), p, p.SessionID, []byte(payload), b.Model, b.Provider, 1, 1, 1,
		contextstate.SessionSaveOptions{
			SessionID: p.SessionID, SessionRevision: &revision,
			Worktree: ns.instance.Worktree, WorktreeInstance: ns.instance,
		}); err != nil {
		t.Fatalf("%s: SaveSession: %v", ns.name, err)
	}
}

func (ns catalogNamespace) advance(t *testing.T, store *SQLite, p contextstate.Principal, b contextstate.BindingRevision, expected contextstate.Revision, reason string, clear bool) {
	t.Helper()
	req := contextstate.AdvanceRequest{
		OperationID: "advance-" + reason, Principal: p, SessionID: p.SessionID,
		Expected: expected, ExpectedBinding: b, NewBinding: b,
		NewSession: expected.Session + 1, NewDurable: expected.Durable + 1, NewSourceSequence: expected.Source,
		ClearActive: clear, Reason: reason, WorktreeInstance: ns.instance,
	}
	if err := store.Advance(context.Background(), req); err != nil {
		t.Fatalf("%s: Advance(%s): %v", ns.name, reason, err)
	}
}

func (ns catalogNamespace) load(store *SQLite, p contextstate.Principal) ([]byte, contextstate.SessionCatalogInfo, error) {
	ctx := context.Background()
	if ns.instance.IsZero() {
		return store.LoadSession(ctx, p, p.SessionID)
	}
	return store.LoadWorktreeSession(ctx, p, p.SessionID, ns.instance)
}

func (ns catalogNamespace) deleteSnapshot(store *SQLite, p contextstate.Principal) error {
	ctx := context.Background()
	if ns.instance.IsZero() {
		return store.DeleteSessionSnapshot(ctx, p, p.SessionID)
	}
	return store.DeleteWorktreeSessionSnapshot(ctx, p, p.SessionID, ns.instance)
}

func (ns catalogNamespace) tombstoned(t *testing.T, store *SQLite, p contextstate.Principal) int {
	t.Helper()
	var tombstoned int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=?`,
		p.WorkspaceID, p.SubjectID, p.SessionID).Scan(&tombstoned); err != nil {
		t.Fatalf("%s: read tombstone: %v", ns.name, err)
	}
	return tombstoned
}

// A session whose turns all committed and which was never /save'd has no
// snapshot row at all - the NORMAL shape for a TUI session. Both namespaces
// must serve it from the live checkpoint, and both must hand back its live
// identity so the caller reclaims instead of forking its next turn into a
// second session.
func TestCatalogNamespaces_ServeTurnOnlyLiveSession(t *testing.T) {
	for _, ns := range catalogNamespaces {
		t.Run(ns.name, func(t *testing.T) {
			store, p, b := ns.open(t)
			ns.commitTurn(t, store, p, b, "turn-1", "only-in-the-checkpoint", contextstate.Revision{})

			payload, info, err := ns.load(store, p)
			if err != nil {
				t.Fatalf("load a turn-only session: %v", err)
			}
			if !bytes.Contains(payload, []byte("only-in-the-checkpoint")) {
				t.Fatalf("payload = %s, want the live checkpoint", payload)
			}
			if info.SessionID != p.SessionID {
				t.Fatalf("info.SessionID = %q, want %q - without the live identity the resume forks", info.SessionID, p.SessionID)
			}
		})
	}
}

// /clear is the affordance users rely on to drop secrets and dead context. It
// leaves an older snapshot on disk, so a loader that prefers the snapshot
// whenever the live payload is empty resurrects the conversation the user
// explicitly purged.
func TestCatalogNamespaces_DoNotResurrectClearedConversation(t *testing.T) {
	for _, ns := range catalogNamespaces {
		t.Run(ns.name, func(t *testing.T) {
			store, p, b := ns.open(t)
			ns.commitTurn(t, store, p, b, "turn-1", "sensitive-pre-clear", contextstate.Revision{})
			ns.saveSnapshot(t, store, p, b, `[{"role":"user","content":"sensitive-pre-clear"}]`, 1)
			ns.advance(t, store, p, b, contextstate.Revision{Session: 1, Durable: 1, Source: 1}, "clear", true)

			payload, _, err := ns.load(store, p)
			if err != nil {
				t.Fatalf("load after clear: %v", err)
			}
			if bytes.Contains(payload, []byte("sensitive-pre-clear")) {
				t.Fatalf("payload = %s, want the cleared conversation to stay gone", payload)
			}
		})
	}
}

// The mirror of the rule above: session_revision is a CONTENT staleness proxy,
// so an advance that changes only the binding (/model, a provider switch) must
// not make a snapshot look superseded. That snapshot is a failed turn's only
// copy.
func TestCatalogNamespaces_KeepSnapshotAcrossBindingAdvance(t *testing.T) {
	for _, ns := range catalogNamespaces {
		t.Run(ns.name, func(t *testing.T) {
			store, p, b := ns.open(t)
			ns.saveSnapshot(t, store, p, b, `[{"role":"user","content":"only-copy-of-this-turn"}]`, 0)
			ns.advance(t, store, p, b, contextstate.Revision{}, "select", false)

			payload, _, err := ns.load(store, p)
			if err != nil {
				t.Fatalf("load after a binding advance: %v", err)
			}
			if !bytes.Contains(payload, []byte("only-copy-of-this-turn")) {
				t.Fatalf("payload = %s, want the snapshot - a binding change supersedes nothing", payload)
			}
		})
	}
}

// Deleting a session must leave nothing loadable AND actually run the
// retention lifecycle, in BOTH the snapshot-present and turn-only shapes: a
// delete that only removes the snapshot hands the whole conversation back
// through the live arm, with its payloads unrevoked and no audit record, while
// reporting success.
func TestCatalogNamespaces_DeleteLeavesNothingLoadable(t *testing.T) {
	for _, ns := range catalogNamespaces {
		for _, shape := range []string{"with snapshot", "turn only"} {
			t.Run(ns.name+"/"+shape, func(t *testing.T) {
				store, p, b := ns.open(t)
				ns.commitTurn(t, store, p, b, "turn-1", "deleted-content", contextstate.Revision{})
				if shape == "with snapshot" {
					ns.saveSnapshot(t, store, p, b, `[{"role":"user","content":"deleted-content"}]`, 1)
				}

				if err := ns.deleteSnapshot(store, p); err != nil {
					t.Fatalf("delete: %v", err)
				}
				if _, _, err := ns.load(store, p); !errors.Is(err, contextstate.ErrSessionNotFound) {
					t.Fatalf("load after delete = %v, want ErrSessionNotFound", err)
				}
				if got := ns.tombstoned(t, store, p); got != 1 {
					t.Fatal("the live row was left untombstoned: payloads unrevoked, no retention audit")
				}
			})
		}
	}
}

// catalogEntryPointsOutsideTheContract are the exported Load*/Delete*Session*
// methods that are deliberately NOT namespace pairs, each with the reason.
// Anything not listed here and not covered by catalogNamespaces fails the
// guard below, so a new namespace cannot be added without joining the table.
var catalogEntryPointsOutsideTheContract = map[string]string{
	"LoadSessionAdmission":         "admission side table, its own contract",
	"LoadWorktreeSessionAdmission": "admission side table, its own contract",
	"DeleteSession":                "the principal's OWN live session, not a catalog entry",
	"DeleteWorktreeSessions":       "instance teardown: deletes every session bound to a worktree",
}

// The conformance table covers the plain and worktree namespaces. A third
// namespace - or a second loader for an existing one - must either join the
// table or be classified as outside the contract, because the alternative is
// what this file exists to prevent: a behaviour implemented for one namespace
// and silently absent from the other.
func TestEveryCatalogEntryPointIsClassified(t *testing.T) {
	covered := map[string]bool{
		"LoadSession": true, "DeleteSessionSnapshot": true,
		"LoadWorktreeSession": true, "DeleteWorktreeSessionSnapshot": true,
	}
	entryPoint := regexp.MustCompile(`^(Load|Delete)\w*Session\w*$`)
	typ := reflect.TypeOf(&SQLite{})
	var unclassified []string
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		if !entryPoint.MatchString(name) || covered[name] {
			continue
		}
		if _, known := catalogEntryPointsOutsideTheContract[name]; known {
			continue
		}
		unclassified = append(unclassified, name)
	}
	if len(unclassified) > 0 {
		t.Fatalf("catalog entry points neither covered by catalogNamespaces nor classified as outside the contract: %s\n"+
			"Add the namespace to catalogNamespaces so the shared assertions run against it, or record why it is exempt in catalogEntryPointsOutsideTheContract.",
			strings.Join(unclassified, ", "))
	}
}
