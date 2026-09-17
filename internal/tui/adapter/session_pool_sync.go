package adapter

// Split from session_pool.go: the chat-sync wiring (attach, reattach after
// login, sync option construction, background watch, and the sync-identity
// anchor), kept together because they share the p.syncSessions /
// p.busReleases bookkeeping and the released-latch discipline described
// below.
// INVARIANT: attachSyncLocked and StartBackgroundWatch read p.released, which ReleaseLeases (session_pool_lifecycle.go) stores under p.mu before draining the pool. Each read must stay inside its caller's p.mu critical section.
// LOCK ORDER: p.mu -> RemoteInputWatcher.mu, never the reverse. attachSyncLocked's watcher.StopSync and ReleaseLeases' watcher.Stop both acquire w.mu while p.mu is (StopSync) or was just held, so no watcher path may call back into the pool under w.mu - see RemoteInputWatcher's own doc comment and Backfill's IsPooled filter pass.

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// AuthorUserIDProvider resolves the CLI's own authenticated principal for
// verifying who queued a remote input, before SessionPool ever forwards one
// through RemoteInputs. A package-level var (like SubagentProgressRegistrar)
// so a test can substitute a fixed identity without a real logged-in session
// or a network call to Whoami.
var AuthorUserIDProvider = chatsync.DefaultAuthorUserIDProvider

// SessionBusRegistrar binds a session's EventBus into the CLI-side
// session-keyed registry (internal/cli/chat.RegisterSessionBus) so
// emitSubagentProgress - package-level, no session of its own - can
// publish that session's subagent lifecycle events onto it. Nil (the
// zero value) is a safe no-op: a caller that never wires this (a test,
// or a build that never imports internal/cli) simply gets no chatsync
// routing, exactly like an unset SubagentProgressRegistrar produces no
// UI routing. Mirrors SubagentProgressRegistrar's indirection shape so
// internal/tui/adapter never imports internal/cli (INV-TUI-29): only
// internal/tui/run, which imports both, wires this at startup.
var SessionBusRegistrar func(sessionID string, bus *events.Bus) (release func())

// attachSyncLocked starts chat sync for one pooled session.
//
// Activation is authentication: a logged-in user syncs, a logged-out one does
// not, and neither state needs a flag or a prompt. `enabled = false` is the
// only way to say no while logged in - see config.ResolvedSync.Active. Every
// refusal here is silent by design: sync failing is never a reason to break
// the local chat the user actually asked for.
func (p *SessionPool) attachSyncLocked(sess *chat.Session) {
	if p.released.Load() || p.res == nil || sess == nil || sess.EventBus == nil {
		return
	}
	id := sess.SessionID
	if id == "" {
		return
	}
	if p.watcher != nil {
		_ = p.watcher.StopSync(id, 2*time.Second)
	}
	if _, exists := p.syncSessions[id]; exists {
		return
	}
	tokens := chatsync.DefaultTokenProvider()
	if !p.res.Sync.Active(tokens != nil) {
		return
	}
	var wsRoot string
	if p.agentState != nil {
		wsRoot = p.agentState.WorkspaceRoot
	}
	opts := poolSyncOptions(sess, id, wsRoot, p.res, tokens)
	p.wireSyncNotices(&opts)
	syncSess, err := chatsync.OpenSession(context.Background(), sess.EventBus, id, opts)
	if err == nil {
		p.syncSessions[id] = syncSess
		p.pushNotice("chat sync is running, uploading to " + chatsync.ResolveEndpoint(p.res.Sync.APIURL).Describe())
		if opts.EnablePolling {
			go p.pumpRemoteInputs(id, syncSess)
		}
		if SessionBusRegistrar != nil {
			p.busReleases[id] = SessionBusRegistrar(id, sess.EventBus)
		}
	}
}

func (p *SessionPool) wireSyncNotices(opts *chatsync.SessionOptions) {
	opts.OnStop = func(reason string) {
		p.pushNotice("chat sync stopped: " + reason)
	}
	opts.OnDegraded = func(reason string) {
		p.pushNotice("chat sync degraded, events are queued locally: " + reason)
	}
	opts.OnRecovered = func() {
		p.pushNotice("chat sync recovered, queued events delivered")
	}
	opts.OnInputRejected = func(id, sessionID, reason string) {
		p.pushNotice("chat sync: refused a remote input: " + reason)
	}
}

// ReattachSyncAfterLogin closes the login-after-session-start sync gap: a
// session created (and pooled) while logged out never gets a chat-sync
// session, because attachSyncLocked's `p.res.Sync.Active(tokens != nil)`
// check is false at construction time and nothing re-checks it later. A
// successful /login flips that check true for every session already in the
// pool, so this re-runs attachSyncLocked for each one - idempotent per
// session via attachSyncLocked's own `p.syncSessions[id]` guard, so a
// session that was already syncing (this is the SECOND session pooled
// after an earlier login, say) gains no duplicate attach.
//
// The session list is snapshotted under p.mu and then the lock is
// RELEASED before iterating: chatsync.OpenSession does real network I/O
// (an HTTP round trip to create or resume the remote session), and holding
// p.mu across that for every pooled session would serialize a multi-session
// pool's login-triggered sync behind one slow or hanging network call,
// blocking every other pool operation (Session, GetOrCreate, IsActive) for
// the duration. Each session's own attach still short-locks around
// attachSyncLocked, matching every other call site's locking discipline.
func (p *SessionPool) ReattachSyncAfterLogin() {
	p.mu.Lock()
	sessions := make([]*chat.Session, 0, len(p.sessions))
	seen := make(map[*chat.Session]struct{}, len(p.sessions))
	for _, sess := range p.sessions {
		if sess == nil {
			continue
		}
		if _, dup := seen[sess]; dup {
			continue
		}
		seen[sess] = struct{}{}
		sessions = append(sessions, sess)
	}
	p.mu.Unlock()

	for _, sess := range sessions {
		p.mu.Lock()
		p.attachSyncLocked(sess)
		p.mu.Unlock()
	}
}

// poolSyncOptions builds the SessionOptions the TUI session pool hands to
// chatsync.OpenSession. It is a separate function so a test can drive the
// exact value production uses, instead of asserting on a hand-built copy that
// can drift away from the wiring it claims to cover.
func poolSyncOptions(sess *chat.Session, id string, wsRoot string, res *config.Resolved, tokens chatsync.TokenProvider) chatsync.SessionOptions {
	// See the matching comment in internal/cli/chat/chat_sync.go: the identity
	// must be resolved before the options, because OutboxDir has to carry the
	// local handle before OpenSession opens the outbox. wsRoot, not
	// sess.SessionDir - see attachSyncLocked's comment for why.
	anchor := chatSyncAnchor(wsRoot)
	identityDir := chatsync.IdentityDir(anchor)
	key := chatsync.IdentityKey(id)
	ident, _ := chatsync.LoadOrCreateIdentity(identityDir, key)

	return chatsync.SessionOptions{
		TokenProvider: tokens,
		ClientOptions: chatsync.ClientOptions{
			BaseURL: chatsync.DefaultBaseURL(res.Sync.APIURL),
		},
		ProjectorOptions: chatsync.ProjectorOptions{
			IncludeToolIO:   res.Sync.IncludeToolIO,
			IncludeThinking: res.Sync.IncludeThinking,
			StreamAssistant: res.Sync.StreamAssistant,
			// See the matching comment in internal/cli/chat/chat_sync.go: both
			// zero values are wrong rather than absent, and this is the only
			// site that can supply them for the TUI surface.
			ErrorMessage: chat.TurnErrorMessage,
			// From the PERSISTED identity, never a per-run random: attach
			// compares this against the writer id on events the server holds
			// past our cursor, so a value that changed every run would read
			// our own previous run as foreign, end the remote session and
			// fork (REVIEW CHANGE 8's permanent data loss).
			WriterID:       ident.WriterID,
			RedactToolArgs: tools.RedactToolArgs(),
		},
		OutboxDir:       chatsync.OutboxDirFor(anchor, ident.LocalHandle),
		LocalHandle:     ident.LocalHandle,
		RemoteSessionID: ident.RemoteSessionID,
		Identity:        chatsync.IdentityRef{Dir: identityDir, Key: key},
		MaxUnflushed:    res.Sync.MaxUnflushed,
		PollWaitSeconds: res.Sync.PollWaitSeconds,
		HeartbeatPeriod: config.SaturatingSeconds(res.Sync.HeartbeatSeconds),
		CreateTitle:     "Session",
		// Remote input (chat-sync "steering") is enabled on the TUI surface
		// only. The original wiring (deleted in 0a709d80) fed server-supplied
		// text straight into conv.Send from THIS package, headlessly, with no
		// identity check and nothing draining the resulting turn's event
		// channel past its 32-event buffer - it deadlocked the agent loop and
		// then every later local keypress (Conversation.Send holds turnMu
		// until the turn goroutine returns). That shape is gone for good.
		//
		// The safe replacement: SessionPool only ever fans a chatsync-VALIDATED
		// RemoteInput (session id, author identity via AuthorUserIDProvider,
		// message shape - all checked in internal/chatsync's InputPoller,
		// never here) into RemoteInputs(). internal/tui/view/screen/conversation -
		// the screen actually rendering a turn - is the sole caller of
		// conv.Send for a remote instruction, draining it through the exact
		// same awaitSessionEvent path a local send uses. See
		// docs/design/ui-isolation.md: internal/tui/adapter is the one bridge
		// INV-TUI-29 allows, so the fan-out lives here; the execution
		// decision does not.
		//
		// Approval mode: a remote-origin turn runs under whatever approval
		// policy this session already has bound (internal/config/bootstrap.go
		// defaults to ApprovalPolicyAuto - every tool call auto-approved, no
		// prompt). Nothing here changes that policy for a remote turn versus
		// a local one; a remote instruction is exactly as trusted as a local
		// keypress once its author identity is verified. Whether that is the
		// right default for a REMOTE-origin turn specifically (as opposed to
		// merely inheriting whatever the local session already runs under)
		// is an explicit open product decision, not one this code makes -
		// see the delivery report.
		AuthorUserIDProvider: AuthorUserIDProvider(),
		EnablePolling:        true,
	}
}

// StartBackgroundWatch begins watch-only polling of up to
// res.Sync.BackgroundWatchMax recent unpooled sessions with a RemoteSessionID.
func (p *SessionPool) StartBackgroundWatch(ctx context.Context) {
	p.mu.Lock()
	if p.released.Load() || p.res == nil {
		p.mu.Unlock()
		return
	}
	tokens := chatsync.DefaultTokenProvider()
	if !p.res.Sync.Active(tokens != nil) {
		p.mu.Unlock()
		return
	}
	var seed *chat.Session
	for _, s := range p.sessions {
		if s != nil {
			seed = s
			break
		}
	}
	if seed == nil {
		p.mu.Unlock()
		return
	}

	var wsRoot string
	if p.agentState != nil {
		wsRoot = p.agentState.WorkspaceRoot
	}

	cfg := WatcherConfig{
		Seed:           seed,
		Tokens:         tokens,
		Res:            p.res,
		WorkspaceRoot:  wsRoot,
		AuthorProvider: AuthorUserIDProvider(),
		Max:            p.res.Sync.BackgroundWatchMax,
		IsPooled: func(sessionID string) bool {
			p.mu.Lock()
			defer p.mu.Unlock()
			_, pooled := p.sessions[sessionID]
			return pooled
		},
		Deliver: func(sessionID string, in chatsync.RemoteInput, ack func()) {
			p.remoteInputs <- ports.RemoteInputEvent{
				ID:          in.ID,
				Kind:        in.Kind,
				SessionID:   sessionID,
				Body:        in.Body,
				ReceivedAt:  in.Received,
				AckReceived: ack,
			}
		},
	}
	watcher := NewRemoteInputWatcher(cfg)
	p.watcher = watcher
	p.mu.Unlock()

	go watcher.Backfill(ctx)
}

// chatSyncAnchor resolves wsRoot into the directory chat-sync keeps its
// identity/outbox files under - wsRoot's .mivia/ namespace, not the bare
// workspace root, so chat-sync's durable state (and, in the outbox, real
// conversation transcript content queued for upload) does not scatter into
// the project tree the user actually works in.
//
// The empty check happens on wsRoot BEFORE NamespacePath, not after:
// workspace.NamespacePath("") returns the RELATIVE ".mivia" (its own doc
// comment says so - correct for its other callers, wrong here), so
// IdentityDir/OutboxDirFor's own empty-storeDir guards would never see an
// empty string and would happily write under cwd's ".mivia" instead of
// refusing - the same class of leak this anchoring exists to close.
func chatSyncAnchor(wsRoot string) string {
	if wsRoot == "" {
		return ""
	}
	return workspace.NamespacePath(wsRoot)
}
