package uiadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// Worktree-session actions on the command runner: the picker's route rows
// and worktree-bound rows dispatch here. Everything heavy lives in
// internal/worktreeroute (shared leaf); this file only resolves the
// repository store and root from live session state, threads the pool's
// pre-bind hook through, and shapes the outcome the screen applies.

// StartInWorktree implements ports.CommandRunner: start a brand-new chat
// session inside the worktree a selected route row stands for. Starting
// twice for the same worktree is allowed - each press creates an
// independent live session scoped to that worktree's instance; nothing
// enforces one-session-per-worktree.
func (r *CommandRunner) StartInWorktree(ctx context.Context, summary ports.SessionSummary) ports.CommandOutcome {
	return r.worktreeSessionOutcome(ctx, summary, true)
}

// ResumeInWorktree implements ports.CommandRunner: resume an existing
// worktree-bound session with its binding re-applied before the surface
// renders. A summary without worktree metadata degrades to SelectSession.
//
// This explicit API deliberately does NOT require WorktreeInstanceID:
// StartInRoute resolves and validates the live instance itself and
// fail-closes on untracked worktrees. The instance gate lives on the
// LISTING-driven paths (pickSelectionCmd, selectWorktreeSummary), where
// bare worktree metadata marks legacy rows that must resume plain.
func (r *CommandRunner) ResumeInWorktree(ctx context.Context, summary ports.SessionSummary) ports.CommandOutcome {
	if summary.Worktree == "" && summary.WorktreeDir == "" {
		return r.SelectSession(ctx, summary.ID)
	}
	return r.worktreeSessionOutcome(ctx, summary, false)
}

func (r *CommandRunner) worktreeSessionOutcome(ctx context.Context, summary ports.SessionSummary, fresh bool) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if r.pool == nil {
		return ports.CommandOutcome{Err: "no session pool available"}
	}
	store, ok := sess.ContextStore().(*storage.SQLite)
	if !ok || store == nil {
		return ports.CommandOutcome{Err: "worktree sessions need a repository context store"}
	}
	root, err := worktreeroute.Root("")
	if err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("resolve repository root: %v", err)}
	}
	// Click-time fence on the pooled early return (resume arm only):
	// when the pooled binding no longer matches the live managed instance,
	// surface fail-closed feedback instead of silently resuming into a
	// stale or foreign checkout. One read-only SELECT; no turn locks.
	if !fresh && summary.Worktree != "" {
		if notice, fenced := r.fencePooledWorktree(ctx, store, root, summary); fenced {
			return ports.CommandOutcome{Err: notice}
		}
	}
	route := worktreeroute.Route{Worktree: summary.Worktree, Dir: summary.WorktreeDir}
	// Carry the instance the LISTING promised into the bind: StartInRoute
	// refuses the request as stale when the live instance differs, so a row
	// rendered before an out-of-band recreate fails with a clear message
	// instead of silently binding whatever is live now. Rows without an
	// instance (explicit API callers) keep the resolve-live behavior.
	if summary.WorktreeInstanceID != "" {
		route.Instance = contextstate.WorktreeInstance{Worktree: summary.Worktree, ID: summary.WorktreeInstanceID}
	}
	// The returned root is the worktree ROOT the binding validated, which
	// is not always summary.WorktreeDir: a session saved in a worktree
	// SUBDIRECTORY must still get tools that see the whole worktree, the
	// way the REPL scopes them. See BindFunc.
	bind := worktreeBindFunc(store, root, route)

	var conv ports.Conversation
	if fresh {
		conv, err = r.pool.CreateFreshInDir(bind, summary.WorktreeDir)
	} else {
		conv, err = r.pool.GetOrCreateInDir(summary.ID, bind, summary.WorktreeDir)
	}
	toolScope := r.pool.takeToolScopeNotice()
	if err != nil {
		action := "start"
		if !fresh {
			action = "resume"
		}
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to %s session in worktree %q: %v", action, summary.Worktree, err)}
	}
	if pooled, ok := conv.(*Conversation); ok && pooled != nil {
		r.SetActiveSession(pooled.Session())
	}
	label := "Resumed"
	if fresh {
		label = "Started new"
	}
	return ports.CommandOutcome{
		Conversation:    conv,
		ClearTranscript: true,
		Notice:          appendToolScope(fmt.Sprintf("%s session in worktree %s.", label, summary.Worktree), toolScope),
	}
}

// worktreeBindFunc is the ONE worktree binding used by every entry point:
// the picker's row-carried route and the store-resolved route a bare id
// gets (SessionPool.storedRouteLocked). It validates the live instance and
// then makes the physical-identity check the DB row cannot - the on-disk
// marker must name the exact instance just bound (REPL parity; catches a
// worktree removed and recreated out-of-band at the same path while the row
// stayed active). The returned root is the worktree ROOT the binding
// validated, which is not always the requested dir: a session saved in a
// worktree SUBDIRECTORY must still get tools that see the whole worktree.
func worktreeBindFunc(store *storage.SQLite, root string, route worktreeroute.Route) BindFunc {
	return func(newSess *chat.Session) (string, error) {
		bound, err := worktreeroute.StartInRoute(context.Background(), newSess, store, root, route)
		if err != nil {
			return "", err
		}
		if err := cliagents.VerifyWorktreeMarker(bound.Dir, bound.Instance); err != nil {
			return "", err
		}
		return bound.Dir, nil
	}
}

// fencePooledWorktree guards the cached-entry early return of a resume:
// the pooled conversation is returned without re-running StartInRoute's
// validation, so this re-checks the live instance cheaply and reports
// fail-closed text when the world changed under the pool. Best-effort -
// probe errors fence the resume fail-closed with a removed-instance
// message (including transient SQL failures); every mismatch string
// names both instance ids where two exist.
func (r *CommandRunner) fencePooledWorktree(ctx context.Context, store *storage.SQLite, root string, summary ports.SessionSummary) (string, bool) {
	pooled := r.pool.Session(summary.ID)
	if pooled == nil {
		return "", false
	}
	binding := pooled.ContextWorktreeBinding()
	if binding.IsZero() {
		return "", false // unbound entry: deliberate pass-through, see control test
	}
	// Same fixed-valid-input guarantee StartInRoute's callers rely on:
	// the route principal cannot fail for a caller-resolved root.
	principal, _ := worktreeroute.Principal(root)
	live, liveErr := store.LiveWorktreeInstance(ctx, principal, summary.Worktree)
	if liveErr != nil || live.State != contextstate.WorktreeActive {
		return removedInstanceText(summary.Worktree), true
	}
	if live.Instance.ID != binding.ID {
		return recreatedInstanceText(summary.Worktree, binding.ID, live.Instance.ID), true
	}
	if err := cliagents.VerifyWorktreeMarker(pooled.ContextWorktreeRoot(), binding); err != nil {
		return fmt.Sprintf("cannot resume session in worktree %q: %v", summary.Worktree, err), true
	}
	return "", false
}

func removedInstanceText(worktree string) string {
	return fmt.Sprintf(
		"cannot resume session in worktree %q: worktree %q was removed - start it anew from /resume",
		worktree, worktree)
}

func recreatedInstanceText(worktree, pooledID, liveID string) string {
	return fmt.Sprintf(
		"cannot resume session in worktree %q: worktree %q was recreated under the same name (pooled %s, live %s) - start a new session from /resume.",
		worktree, worktree, pooledID, liveID)
}

// selectWorktreeSummary finds the instance-bound row matching id in
// listing metadata. Which rows count as bound is
// SessionSummary.WorktreeBound - the single predicate shared with the
// picker's enter-key dispatch - so the typed-/resume router and the
// picker cannot disagree on which rows take the instance-scoped path.
// Pure so tests table-drive it directly.
func selectWorktreeSummary(rows []ports.SessionSummary, id string) (ports.SessionSummary, bool) {
	for _, s := range rows {
		if s.ID == id && s.WorktreeBound() {
			return s, true
		}
	}
	return ports.SessionSummary{}, false
}

// StartInNewWorktree implements ports.CommandRunner: creates a managed
// worktree (auto-named when name is empty) and starts a new session in it.
// Duplicate errors surface as Err; the user presses the shortcut again.
func (r *CommandRunner) StartInNewWorktree(ctx context.Context, name string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil {
		return ports.CommandOutcome{Err: "no active session"}
	}
	if r.pool == nil {
		return ports.CommandOutcome{Err: "no session pool available"}
	}
	store, ok := sess.ContextStore().(*storage.SQLite)
	if !ok || store == nil {
		return ports.CommandOutcome{Err: "worktree sessions need a repository context store"}
	}
	root, err := worktreeroute.Root("")
	if err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("resolve repository root: %v", err)}
	}
	if name == "" {
		// The random tail keeps two presses inside one wall-clock second
		// from colliding on the timestamp; the prefix stays sortable.
		name = "wt-" + time.Now().UTC().Format("0102-150405") + "-" + randomNameTail()
	}
	sanitized, err := vcs.SanitizeName(name)
	if err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("invalid worktree name %q: %v", name, err)}
	}
	name = sanitized
	if err := cliagents.CreateManagedWorktreeForPool(store, root, name); err != nil {
		return ports.CommandOutcome{Err: fmt.Sprintf("failed to create worktree %q: %v", name, err)}
	}
	// No canonicalization here: both consumers of WorktreeDir canonicalize
	// themselves (StartInRoute for the binding, adoptWorktreeToolsLocked
	// for the tool registry), so the freshly created path is passed as-is.
	dir := filepath.Join(workspace.WorktreesDir(root), name)
	summary := ports.SessionSummary{ID: name, Worktree: name, WorktreeDir: dir}
	return r.worktreeSessionOutcome(ctx, summary, true)
}

// randomNameTail returns four hex characters of crypto randomness for the
// auto-generated worktree name. Collisions within one second would send
// the user into a duplicate-name failure whose only remedy regenerates
// the same name.
func randomNameTail() string {
	var b [2]byte
	// crypto/rand.Read is documented to always fill b and never return an
	// error (it crashes the program on a broken randomness source).
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
