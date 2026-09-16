package adapter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/uievent"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// SessionPool manages active and resumed sessions in memory.
// It allows background sessions to keep running while the user switches
// freely between them.
type SessionPool struct {
	mu           sync.Mutex
	sessions     map[string]*chat.Session
	convs        map[string]*Conversation
	syncSessions map[string]*chatsync.SyncSession
	// busReleases holds the release func returned by SessionBusRegistrar for
	// each session id whose bus was successfully registered, parallel to
	// syncSessions (one entry per attachSyncLocked success). ReleaseLeases
	// drains it alongside its existing sync-session teardown so a pooled
	// session's chat-sync bus binding does not outlive the pool.
	busReleases map[string]func()
	// released latches once ReleaseLeases has drained the pool.
	// ReattachSyncAfterLogin snapshots p.sessions and re-locks per session,
	// so a /login completing while the TUI quits could otherwise re-run
	// attachSyncLocked after the drain and attach a SyncSession nothing
	// will ever stop - the resurrection window this guard closes.
	released   atomic.Bool
	res        *config.Resolved
	agentState *agents.AgentSessionState
	// agentStates is the per-entry agent-selection state (Selected,
	// SkillScope, TierPlan, Baseline*) for every pooled session, keyed by
	// session ID. The launch entry is registered with agentState itself, not
	// a fork of it; every later entry (CreateFresh, CreateFreshInDir,
	// GetOrCreate, GetOrCreateInDir) gets agentState.Fork() so one session's
	// /agent switch or deferred-tool admission cannot rewrite another
	// pooled session's policy through a shared pointer (bug-audit "pooled
	// worktree sessions share mutable agent state"). See AgentState.
	agentStates map[string]*agents.AgentSessionState
	toolsOn     bool
	// threads is the one SubagentThreads registry shared by every
	// Conversation the pool creates or resumes, so the activity panel's
	// thread dialog (wired once, at startup, to this same instance) can
	// resolve any pooled session's dispatched subagents - past history via
	// SetSubagents' PopulateFromToolCalls and live events via Send's
	// newTurnHandler. A Conversation the pool never wires to this registry
	// is invisible to the dialog: see Threads.
	threads *SubagentThreads

	// notices is the pool-wide advisory stream (ports.Notices). It is
	// created once, never closed, and written only through pushNotice.
	notices chan uievent.Event
	// workflowStatusCh carries replaceable workflow liveness, separately from
	// notices. It must not share that queue: the controller emits a heartbeat
	// per running step every 15s, and pushing those into the 16-slot advisory
	// buffer evicted the transitions the operator actually needs to read.
	workflowStatusCh chan uievent.Event

	// remoteInputs is the pool-wide steering stream (ports.RemoteInputs).
	// Created once, never closed, fed by one pumpRemoteInputs goroutine per
	// attached sync session - see attachSyncLocked and RemoteInputs.
	remoteInputs chan ports.RemoteInputEvent

	// watcher runs background InputPollers for unpooled saved sessions.
	watcher *RemoteInputWatcher

	// Worktree registries are memoized by canonical root. They are separate
	// from the launch session's registry and are released by CloseAll.
	regByRoot           map[string]*tools.Registry
	regCloses           []func()
	buildSer            sync.Mutex
	lastCreated         *Conversation
	lastToolScopeNotice string
	launchRootDone      bool
	launchRootVal       string

	// workflowSubs holds the pool's workflow-progress subscriptions, keyed by
	// the bus they are registered on so arming is idempotent across the
	// several places a session with a bus enters the pool. See
	// workflow_notices.go.
	workflowSubs map[*events.Bus]*events.Subscription
	// workflowStatus is the liveness of the run currently executing, folded
	// from the same subscription and published as a replaceable status event.
	// It owns its own lock (see workflowStatusTracker) and is deliberately
	// NOT guarded by p.mu: the bus handler must never contend with a pool
	// operation.
	workflowStatus workflowStatusTracker

	// resuming tracks GetOrCreateInDir calls in flight, keyed by the
	// requested id, so a second caller for the SAME id joins the first
	// instead of building an independent, orphaned twin. See resumeInFlight.
	resuming map[string]*resumeInFlight
}

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

// Threads returns the SubagentThreads registry every pooled Conversation is
// wired to. Callers building the UI (internal/tui/run) pass this same
// instance to Screen.SetSubagentThreads so the dialog resolves whichever
// session is currently active, including one reached by /resume or /new.
func (p *SessionPool) Threads() *SubagentThreads {
	return p.threads
}

// Session returns the underlying chat.Session for a session ID, or nil if not present.
func (p *SessionPool) Session(id string) *chat.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

// IsActive reports whether the session with the given ID has a turn
// currently in flight. A session this process has never loaded into the
// pool cannot be active from here, so it reports false.
func (p *SessionPool) IsActive(id string) bool {
	p.mu.Lock()
	conv, ok := p.convs[id]
	p.mu.Unlock()
	if !ok {
		return false
	}
	return conv.IsActive()
}

// NewSessionPool constructs a SessionPool seeded with the initial session.
func NewSessionPool(initialSess *chat.Session, res *config.Resolved, agentState *agents.AgentSessionState, toolsOn bool) *SessionPool {
	pool := &SessionPool{
		sessions:         make(map[string]*chat.Session),
		convs:            make(map[string]*Conversation),
		syncSessions:     make(map[string]*chatsync.SyncSession),
		busReleases:      make(map[string]func()),
		res:              res,
		agentState:       agentState,
		agentStates:      make(map[string]*agents.AgentSessionState),
		toolsOn:          toolsOn,
		threads:          NewSubagentThreads(),
		notices:          make(chan uievent.Event, syncNoticeBuffer),
		workflowStatusCh: make(chan uievent.Event, 1),
		remoteInputs:     make(chan ports.RemoteInputEvent, remoteInputBuffer),
		workflowSubs:     make(map[*events.Bus]*events.Subscription),
	}
	if initialSess != nil {
		if res != nil {
			initialSess.SetBindingFactory(sessionBindingFactory(initialSess, res, agentState))
		}
		id := initialSess.SessionID
		conv := NewConversation(initialSess)
		pool.wireContentResolver(agentState)
		conv.SetSubagents(pool.threads)
		pool.sessions[id] = initialSess
		pool.convs[id] = conv
		if agentState != nil {
			pool.agentStates[id] = agentState
		}
		pool.attachSyncLocked(initialSess)
		pool.watchWorkflowProgressLocked(initialSess.EventBus)
	}
	return pool
}

// CreateFresh creates a brand-new session, inheriting runtime state (tools,
// context store, context manager, event bus) from the first existing pool
// member. It does NOT call Load — the session starts empty.
// The new conversation is registered in the pool and returned.
//
// The wiring goes through wireEntryLocked, the SAME path every other pooled
// entry takes (CreateFreshInDir, GetOrCreateInDir), rather than a hand-rolled
// copy of it. The copy this replaces omitted the surface publication, and a
// /new session was the one pooled entry that ran without a dispatcher of its
// own: the agent loop then fell back to a bare
// runtime.NewToolDispatcher(registry, runtime.Policy{}) (agentloop_run.go's
// ensureSDKDispatcher), so its tool calls ran with no approval snapshot, no
// PreToolUse/PostToolUse lifecycle hooks, no operator denylist and no result
// caps - while the dispatcher-owned session tools it inherited from the
// LAUNCH registry (dispatch_tasks and the messaging/ledger catalog) still
// executed against the LAUNCH session, so work dispatched from the new
// conversation reported into the old one. Same defect class as
// c20e7b1b's worktree adoption fix, on the path that fix did not cover.
func (p *SessionPool) CreateFresh() (ports.Conversation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.res == nil {
		return nil, fmt.Errorf("no config provided")
	}

	sess := p.newEntrySessionLocked()
	entryState := p.wireEntryLocked(sess, "", "", true, false)

	conv := NewConversation(sess)
	p.wireContentResolver(entryState)
	conv.SetSubagents(p.threads)
	id := sess.SessionID
	p.sessions[id] = sess
	p.convs[id] = conv
	p.bindEntryStateLocked(id, entryState)
	p.lastCreated = conv
	p.attachSyncLocked(sess)
	return conv, nil
}

// GetOrCreate retrieves an active conversation or instantiates a new session
// loaded from the persisted session store.
func (p *SessionPool) GetOrCreate(sessionID string) (ports.Conversation, error) {
	return p.GetOrCreateInDir(sessionID, nil, "")
}

// liveEntryForResolvedLocked reports the live entry, if any, already
// registered under the id Load resolved the session to when that differs
// from the id the caller asked for (sanitization, or a projection that
// resolved to its live identity). Publishing a second Session/Conversation
// under that key would clobber the live one: it stays reachable from the
// UI that holds it but not from the pool, its lease never releases, and two
// divergent copies of one persisted session both save. The caller returns
// the existing entry instead. Callers hold p.mu.
func (p *SessionPool) liveEntryForResolvedLocked(requested string, sess *chat.Session) *Conversation {
	if sess.SessionID == "" || sess.SessionID == requested {
		return nil
	}
	return p.convs[sess.SessionID]
}

// publishEntryLocked registers a just-built entry under the requested id
// and, when Load resolved the session to a different id, under that id too -
// the same entry and the same private state under both keys, never a
// second fork. Callers hold p.mu and have checked liveEntryForResolvedLocked.
func (p *SessionPool) publishEntryLocked(requested string, sess *chat.Session, conv *Conversation, state *agents.AgentSessionState) {
	p.sessions[requested] = sess
	p.convs[requested] = conv
	p.bindEntryStateLocked(requested, state)
	// A pool built before its launch session had a bus (an embedding, or a
	// test) still gets its workflow progress the moment a session carrying
	// one is published. Idempotent per bus.
	p.watchWorkflowProgressLocked(sess.EventBus)
	if sess.SessionID != "" && sess.SessionID != requested {
		p.sessions[sess.SessionID] = sess
		p.convs[sess.SessionID] = conv
		p.bindEntryStateLocked(sess.SessionID, state)
	}
}

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
