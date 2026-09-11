// Package cliautomations implements the `mivia automations` CLI surface:
// list/show/run/serve over the internal/automation backend, driven
// headlessly (no TUI, no pooled multi-session cache) via HeadlessSpawner.
package cliautomations

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// HeadlessSpawner is the non-TUI implementation of automation.SessionSpawner
// used by `mivia automations serve` and `mivia automations run <id>`.
// Where the TUI wires the executor to its own live multi-session cache,
// HeadlessSpawner builds a fresh *chat.Session directly (via
// composition.BuildSession) and wraps it with uiadapter.NewConversation -
// a small, cache-free constructor that implements the full
// ports.Conversation surface with zero reference to any TUI cache type.
// This package never imports or references the TUI's pooled cache type,
// its bind-func alias, or its constructor - verified by a source-guard
// test (spawner_test.go). SetApprovalOverride mirrors exactly what the
// TUI cache's own approval override setter does internally
// (sess.ApprovalGate = gate; sess.SetApprovalPolicy(policy)), both plain
// exported *chat.Session members.
//
// Resource lifecycle and its LOAD-BEARING sequential-dispatch invariant
// are documented in full in docs/design/automations.md's chunk 9 section
// (search "HeadlessSpawner"): in short, composition.BuildSession opens a
// per-session *storage.SQLite that automation.SessionSpawner's 2-method
// interface gives no end-of-run signal to close, so HeadlessSpawner
// tracks a SINGLE in-flight session (not an ID-keyed map), correct only
// because Service.Serve fires automations strictly sequentially and
// `run <id>` is one-shot. This is safe against a dead prior session but
// NOT safe against a live one from a future concurrent-dispatch design -
// see the design doc before ever parallelizing Serve's dispatch loop.
type HeadlessSpawner struct {
	mu      sync.Mutex
	current *storage.SQLite // the one in-flight run's store; nil when idle
	// lastSession is the *chat.Session CreateFreshInDir most recently
	// bound, tracked alongside `current` so SetApprovalOverride (which
	// automation.SessionSpawner's interface identifies only by an opaque
	// sessionID string - conv.ID(), i.e. sess.SessionID) has a concrete
	// session to apply the override to without this package needing an
	// id->session lookup map of its own. Cleared alongside `current` by
	// CloseLastRun so a stale reference cannot outlive its store.
	lastSession *chat.Session
	root        string
	res         *config.Resolved
}

// NewHeadlessSpawner builds a spawner rooted at workspaceRoot, using res
// for every session it builds.
func NewHeadlessSpawner(workspaceRoot string, res *config.Resolved) (*HeadlessSpawner, error) {
	if workspaceRoot == "" {
		return nil, fmt.Errorf("cliautomations: NewHeadlessSpawner requires a non-empty workspace root")
	}
	if res == nil {
		return nil, fmt.Errorf("cliautomations: NewHeadlessSpawner requires a non-nil config")
	}
	return &HeadlessSpawner{root: workspaceRoot, res: res}, nil
}

// buildCompleter constructs a provider.Completer from h.res: a working
// completer when the provider resolves, otherwise a nil completer -
// composition.BuildSession accepts one for construction, but a session
// built with it cannot run a turn until wired with a real provider.
func (h *HeadlessSpawner) buildCompleter() provider.Completer {
	if h.res.ProviderName == "" {
		return nil
	}
	comp, err := provider.New(h.res)
	if err != nil {
		return nil
	}
	return comp
}

// storePathFor resolves the checkpoint store path a spawned session
// uses: root's own clichat.ContextStorePath(root, cfg), the SAME store
// openAutomationStore (automations.go) opens for the Service's run
// records.
//
// Deliberately ignores any worktree directory a run executes in: a
// worktree run must share the root/global automation and session store,
// not a per-worktree-namespaced one, so CLI and TUI run history stay
// unified regardless of where a step executes. This is a change from
// the prior behavior (workspace.ContextStorePath(dir), namespaced by the
// worktree's own directory), which forked a worktree run's session
// history into its own SQLite file, invisible to `mivia automations
// runs` and to the TUI's history for the same workspace.
func storePathFor(root string, cfg config.SubagentConfig) string {
	return clichat.ContextStorePath(root, cfg)
}

// CreateFreshInDir satisfies automation.SessionSpawner: builds a fresh
// session rooted at dir (empty means workspaceRoot) via
// composition.BuildSession, invokes bind if non-nil, wraps the result
// with uiadapter.NewConversation, records the opened store as `current`,
// and returns the conversation.
//
// Defensive close-before-overwrite: if `current` is already non-nil on
// entry (meaning some earlier run's CloseLastRun was skipped), that
// stale store is closed first (its close error is LOGGED, not silently
// discarded, since a non-nil `current` at entry is itself a signal worth
// surfacing) before being overwritten - see the type doc comment for why
// this is only safe against a DEAD prior session, never a live one. A
// bind error discards the newly-opened session and its store immediately
// (closed, not retained as `current`) rather than leaking a half-used one.
func (h *HeadlessSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	workDir := dir
	if workDir == "" {
		workDir = h.root
	}
	wsRoot, err := workspace.Open(workDir)
	if err != nil {
		return nil, fmt.Errorf("cliautomations: open workspace %q: %w", workDir, err)
	}

	sess, store, _, err := composition.BuildSession(composition.SessionInput{
		Config:      h.res,
		Completer:   h.buildCompleter(),
		Registry:    composition.RegistryInput{Workspace: wsRoot},
		StorePath:   storePathFor(h.root, h.res.Subagents),
		WorkspaceID: workDir,
	})
	if err != nil {
		return nil, fmt.Errorf("cliautomations: build session: %w", err)
	}

	if bind != nil {
		if _, bindErr := bind(sess); bindErr != nil {
			_ = store.Close()
			return nil, fmt.Errorf("cliautomations: bind fresh session: %w", bindErr)
		}
	}

	h.mu.Lock()
	staleSession, staleStore := h.lastSession, h.current
	h.current = store
	h.lastSession = sess
	h.mu.Unlock()
	if staleStore != nil {
		// Same leak class CloseLastRun below closes: a session's
		// composition.BuildSession wiring arms a context-lease heartbeat
		// goroutine (SetContextStore/SetContextManager) that runs forever
		// until released, independent of the SQLite store handle it also
		// holds - closing only the store leaves that goroutine running,
		// pinning the whole abandoned session (registry, dispatcher,
		// closed store) and waking every heartbeat tick forever to retry
		// a renew against a database that is now closed. Release before
		// close, exactly like CloseLastRun.
		if staleSession != nil {
			staleSession.ReleaseContextLease(context.Background())
		}
		if closeErr := staleStore.Close(); closeErr != nil {
			log.Printf("cliautomations: HeadlessSpawner: closing stale leftover session store: %v", closeErr)
		}
	}

	return uiadapter.NewConversation(sess), nil
}

// SetApprovalOverride satisfies automation.SessionSpawner. Does NOT
// touch `current` - approval posture is per-session configuration, not
// lifetime management. sessionID is unused (a headless run drives
// exactly one session at a time, identified structurally via
// `lastSession` rather than by a lookup map), but the parameter is kept
// to satisfy the interface.
func (h *HeadlessSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	h.mu.Lock()
	sess := h.lastSession
	h.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("cliautomations: set approval override: no active session")
	}
	sess.ApprovalGate = gate
	sess.SetApprovalPolicy(policy)
	return nil
}

// CloseLastRun closes and forgets whatever session store CreateFreshInDir
// most recently opened and has not yet closed. Takes NO session ID by
// design. Idempotent: a second call, or a call when idle, is a no-op
// returning nil.
//
// Releases the session's context-lease heartbeat BEFORE closing its
// store: composition.BuildSession arms a heartbeat goroutine
// (chat.Session.contextHeartbeat, via SetContextStore/SetContextManager)
// that runs independently of the store handle and never stops on its
// own - closing only the store leaves it running forever, pinning the
// entire abandoned session (registry, dispatcher, closed store) and
// retrying a renew against a database that no longer exists every tick.
// A long-running `automations serve` daemon that never called this
// would accumulate one such goroutine per fire, unboundedly, for its
// entire lifetime (confirmed by audit: 117 fires -> 117 leaked
// heartbeat goroutines over 100s). ReleaseContextLease is a no-op when
// no heartbeat was ever armed (e.g. a session whose bind never set a
// context store), so this is safe to call unconditionally.
func (h *HeadlessSpawner) CloseLastRun() error {
	h.mu.Lock()
	sess := h.lastSession
	store := h.current
	h.current = nil
	h.lastSession = nil
	h.mu.Unlock()
	if sess != nil {
		sess.ReleaseContextLease(context.Background())
	}
	if store == nil {
		return nil
	}
	return store.Close()
}

var _ automation.SessionSpawner = (*HeadlessSpawner)(nil)
