// Package worktreeroute resolves a repository's registered worktree launch
// routes and binds chat sessions to them. It exists as a shared
// leaf so internal/uiadapter can reach worktree-route state without
// importing internal/cliworktree or internal/clichat - the UI isolation
// policy (.mivia/policy/import-layers.json) forbids both edges.
// internal/cliworktree imports this package one-way for the shared route
// principal; this package never imports it back, and it carries no CLI
// surface of its own.
package worktreeroute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// Route is one launch target: a worktree directory a fresh or resumed
// chat session can be bound to.
type Route struct {
	// Worktree is the mivia worktree base name (never a path).
	Worktree string
	// Dir is the worktree's canonical absolute directory.
	Dir string
	// Instance is the managed worktree instance when storage tracks one.
	Instance contextstate.WorktreeInstance
}

// WorkspaceID is a repository's durable catalog identity, derived from the
// canonicalized root directory. Keep it byte-identical to internal/cli's
// contextWorkspaceID: chat_sessions rows are keyed by this digest, and any
// drift strands previously stored sessions. A drift-guard test lives in
// internal/clichat.
func WorkspaceID(root string) string {
	resolved, err := filepath.Abs(root)
	if err != nil {
		resolved = filepath.Clean(root)
	}
	if linked, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = linked
	}
	digest := sha256.Sum256([]byte(resolved))
	return "workspace-" + hex.EncodeToString(digest[:8])
}

// Principal derives the repository-level identity every worktree-route row
// is stored and listed under.
func Principal(root string) (contextstate.Principal, error) {
	return contextstate.NewPrincipal(WorkspaceID(root), "worktree-routes", "local-user")
}

// Root resolves dir (default ".") to the main repository root, matching how
// the CLI resolves its workspace before routing.
func Root(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	root, err := vcs.MainRepoRoot(dir)
	if err != nil {
		return "", fmt.Errorf("resolve repository root from %s: %w", dir, err)
	}
	return root, nil
}

// CanonicalDir evaluates symlinks in dir so it compares equal to the
// canonical path storage recorded for the same worktree. Mirrors
// cliworktree.CanonicalMarkerRoot's resolution without its strict
// input-already-canonical requirement: picker rows come from persisted
// strings that may predate a symlink change under them.
func CanonicalDir(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("worktree dir %q is not absolute", dir)
	}
	linked, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve worktree dir %s: %w", dir, err)
	}
	return filepath.Clean(linked), nil
}

// StartInRoute binds sess to rt the way the REPL's repository binding does:
// validate the live managed instance and retain the binding BEFORE any
// context store is installed. It deliberately does not write route rows:
// registration happens once per worktree at creation or adoption time (the
// active instance row StartInRoute validates is registered in the same
// transaction that inserts the route row), and an extra subject-scope
// upsert here would show the same worktree twice in the catalog.
//
// rt.Dir may name a SUBDIRECTORY of the worktree (a bound session saved
// from one): the binding then carries the worktree's canonical root as its
// root and the subdirectory as the session dir, mirroring the REPL's
// root/dir split. The returned Route carries the validated live instance
// and the canonical worktree ROOT, so callers can run further checks (the
// TUI adds an on-disk marker comparison) against the exact bound identity.
func StartInRoute(ctx context.Context, sess *chat.Session, store *storage.SQLite, root string, rt Route) (Route, error) {
	if sess == nil {
		return Route{}, fmt.Errorf("session is required")
	}
	if rt.Worktree == "" || rt.Dir == "" {
		return Route{}, fmt.Errorf("route requires a worktree name and directory")
	}
	// Same fixed-valid-input guarantee internal/cliworktree documents on
	// removeWorktreeRouteInStore: Principal cannot fail for a
	// caller-supplied root (bounded digest, fixed literal fields), so the
	// error is discarded rather than threaded through every caller.
	principal, _ := Principal(root)
	live, err := store.LiveWorktreeInstance(ctx, principal, rt.Worktree)
	if err != nil {
		return Route{}, fmt.Errorf("bind worktree %q: %w", rt.Worktree, err)
	}
	if !rt.Instance.IsZero() && rt.Instance != live.Instance {
		return Route{}, fmt.Errorf("bind worktree %q: stale instance in request", rt.Worktree)
	}
	rt.Instance = live.Instance
	dir, err := CanonicalDir(rt.Dir)
	if err != nil {
		return Route{}, err
	}
	wtRoot := filepath.Clean(live.CanonicalPath)
	if dir != wtRoot && !strings.HasPrefix(dir, wtRoot+string(filepath.Separator)) {
		return Route{}, fmt.Errorf("bind worktree %q: directory %s is outside the worktree root %s", rt.Worktree, dir, wtRoot)
	}
	rt.Dir = wtRoot
	if err := store.ValidateActiveWorktreeInstance(ctx, principal, rt.Instance, wtRoot); err != nil {
		return Route{}, fmt.Errorf("validate worktree %q binding: %w", rt.Worktree, err)
	}
	if err := sess.SetContextWorktreeBindingAt(rt.Instance, wtRoot, dir); err != nil {
		return Route{}, err
	}
	return rt, nil
}
