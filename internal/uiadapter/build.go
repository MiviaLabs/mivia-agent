package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Input carries every value New needs to wire a real chat.Session behind
// the ports.Conversation seam. Most fields are direct copies of what
// the CLI chat command already supplies; the seam exists so cmd/mivia-ui
// can do the same wiring without importing internal/cli or any of its
// split sub-packages (cliworktree, clichat, etc.).
type Input struct {
	// Resolved is the workspace-resolved configuration. Required; a
	// nil Resolved returns an error naming "resolved" so a CLI caller
	// can surface a precise diagnostic.
	Resolved *config.Resolved
	// WorkspaceRoot is the directory hooks and config tools anchor at.
	// Required iff HooksConfigured is true: with no hooks, the empty
	// string is allowed. Returning an error here keeps a silent
	// mis-wiring impossible.
	WorkspaceRoot string
	// Workspace is the workspace.Root the tool registry uses for
	// filesystem scoping. Optional; nil yields a registry with no
	// filesystem tools.
	Workspace *workspace.Root
	// MCPConfig selects the MCP servers attached after the registry is
	// built. Disabled (Enabled=false) is a no-op, not an error.
	MCPConfig config.MCPConfig
	// SessionID is the checkpoint principal's subject scope. Empty
	// defaults to chat.Session.SessionID inside BuildSession.
	SessionID string
	// StorePath is the SQLite checkpoint store BuildSession opens.
	// Required; an empty path returns an error naming "store path".
	StorePath string

	// Dispatcher tunables. BuildDispatcher passes them through; zero
	// means "use the runtime defaults".
	MaxDepth       int
	MaxRetries     int
	MaxInputBytes  int
	MaxOutputBytes int
	MaxBudget      int

	// Completer is the provider completer the chat session's initial
	// binding runs on. May be nil (chat.NewSession accepts a nil
	// completer for construction); a nil completer cannot run a turn.
	Completer provider.Completer
	// RedactionPolicy is the privacy redaction policy forwarded to MCP
	// server output handling. Nil means the workspace configured none
	// and MCP redaction is skipped (matching internal/cli's default).
	RedactionPolicy *redact.Policy

	// HooksConfigured reports whether the dispatcher should install
	// lifecycle hook closures. False (or an empty WorkspaceRoot) means
	// nil compare per invocation, no hook overhead at all - the same
	// contract the historical cli path held.
	HooksConfigured bool
	// HooksGroups returns the runnable hook groups for the current
	// session. Required iff HooksConfigured is true; nil with
	// HooksConfigured true is treated as "no groups".
	HooksGroups func() []hooks.Group
	// NoteHookWarnings receives runtime diagnostics from hooks that
	// actually executed, for the caller to surface (e.g. a /hooks
	// listing). Nil is safe: warnings are simply dropped.
	NoteHookWarnings func([]string)
}

// Adapter is the production handle cmd/mivia-ui (or a future CLI
// surface) drives a real chat.Session through the ports.Conversation
// seam. The embedded *Conversation exposes every Conversation method
// (Send, History, Model, ContextUsage, Title). The store and mcp fields
// are kept so the cleanup closure the constructor returns can close
// both, and so a future phase that surfaces checkpoint or MCP state in
// the UI does not need to plumb new fields through New's signature.
type Adapter struct {
	*Conversation
	// store is the SQLite checkpoint store BuildSession opened. The
	// cleanup closure closes it.
	store *storage.SQLite
	// mcp is the MCP manager attached to the registry. Nil when MCP
	// is disabled in config; cleanup is a no-op in that case.
	mcp *mcp.Manager
}

// New wires every Input field into a real chat.Session and returns it
// wrapped in an Adapter. The returned cleanup closes the checkpoint
// store and the MCP manager (the latter only when MCP was enabled). On
// any error the function returns (nil, nil, err): no partial resources
// leak, because BuildSession closes its own store on error and
// AttachMCPServers closes its manager on error.
//
// The principal returned by BuildSession is captured but not stored on
// the Adapter; future phases that surface checkpoint state will add a
// field rather than re-running the build to surface it.
func New(ctx context.Context, in Input) (*Adapter, func(), error) {
	if err := validateInput(in); err != nil {
		return nil, nil, err
	}
	registry, err := composition.BuildRegistry(composition.RegistryInput{Workspace: in.Workspace})
	if err != nil {
		return nil, nil, fmt.Errorf("uiadapter: build registry: %w", err)
	}
	mcpMgr, mcpCleanup, err := composition.AttachMCPServers(registry, in.MCPConfig, in.RedactionPolicy, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("uiadapter: attach MCP servers: %w", err)
	}
	sess, store, _, err := buildSessionWithRegistry(in, registry)
	if err != nil {
		// BuildSession closes its own store on error; the MCP manager
		// is still open and must be closed before we return.
		if mcpMgr != nil {
			mcpCleanup()
		}
		return nil, nil, fmt.Errorf("uiadapter: build session: %w", err)
	}
	conv := NewConversation(sess)
	if in.Resolved != nil {
		conv.SetNoticeOptions(TranslateOptions{
			ShowIterationNotices:   in.Resolved.ShowIterationNotices,
			ShowPromptCacheNotices: in.Resolved.ShowPromptCacheNotices,
		})
	}
	return &Adapter{
		Conversation: conv,
		store:        store,
		mcp:          mcpMgr,
	}, newCleanup(store, mcpMgr, mcpCleanup), nil
}

// validateInput enforces the three pre-conditions New requires. Each
// rule names its missing field in the error message so a caller can
// pinpoint the gap without diffing against Input's doc.
func validateInput(in Input) error {
	if in.Resolved == nil {
		return fmt.Errorf("uiadapter: resolved config is required")
	}
	if in.StorePath == "" {
		return fmt.Errorf("uiadapter: store path is required")
	}
	if in.HooksConfigured && in.WorkspaceRoot == "" {
		return fmt.Errorf("uiadapter: workspace root is required when hooks are configured")
	}
	return nil
}

// buildSessionWithRegistry composes the dispatcher and session inputs
// around the registry New built (and that AttachMCPServers may have
// merged MCP tools into). The registry is passed through both via the
// dispatcher's Registry field and via composition.SessionInput's
// PrebuiltRegistry seam (Phase 3 amendment AR-1) so MCP-merged tools
// are visible to the dispatcher.
func buildSessionWithRegistry(in Input, registry *tools.Registry) (*chat.Session, *storage.SQLite, contextstate.Principal, error) {
	return composition.BuildSession(composition.SessionInput{
		Config:           in.Resolved,
		Completer:        in.Completer,
		PrebuiltRegistry: registry,
		Dispatcher: composition.DispatcherInput{
			Registry:         registry,
			MaxDepth:         in.MaxDepth,
			MaxRetries:       in.MaxRetries,
			MaxInputBytes:    in.MaxInputBytes,
			MaxOutputBytes:   in.MaxOutputBytes,
			MaxBudget:        in.MaxBudget,
			WorkspaceRoot:    in.WorkspaceRoot,
			HooksConfigured:  in.HooksConfigured,
			HookGroups:       in.HooksGroups,
			NoteHookWarnings: in.NoteHookWarnings,
		},
		EventBus:    nil,
		StorePath:   in.StorePath,
		WorkspaceID: in.WorkspaceRoot,
		SubjectID:   in.SessionID,
	})
}

// newCleanup returns a cleanup func that closes both the store and the
// MCP manager on shutdown. Each close runs under its own defer-recover
// so a panic in one (e.g. a third-party SQLite driver fault during
// shutdown) does not skip the other. Cleanup is best-effort; the
// caller has a working Adapter and the worst case is a leaked resource
// on shutdown, which the OS reaps at process exit.
//
// Two independent per-resource closures, each with its own
// defer-recover, called in sequence by the outer cleanup: this
// ordering is what guarantees panic-safety. The original ordering
// installed the inner defer-recover INSIDE the if-mcpMgr-not-nil
// branch, AFTER store.Close() ran, so a panic in store.Close() would
// skip mcpCleanup. TestNew_CleanupRunsBothClosesOnPanic in
// build_test.go pins this invariant.
func newCleanup(store *storage.SQLite, mcpMgr *mcp.Manager, mcpCleanup func()) func() {
	cleanupStore := func() {
		defer func() { _ = recover() }()
		_ = store.Close()
	}
	cleanupMCP := func() {
		if mcpMgr == nil {
			return
		}
		defer func() { _ = recover() }()
		mcpCleanup()
	}
	return func() {
		cleanupStore()
		cleanupMCP()
	}
}
