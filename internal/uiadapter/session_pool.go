package uiadapter

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
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
	agentState *cliagents.AgentSessionState
	toolsOn    bool
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

	// remoteInputs is the pool-wide steering stream (ports.RemoteInputs).
	// Created once, never closed, fed by one pumpRemoteInputs goroutine per
	// attached sync session - see attachSyncLocked and RemoteInputs.
	remoteInputs chan ports.RemoteInputEvent

	// watcher runs background InputPollers for unpooled saved sessions.
	watcher *RemoteInputWatcher
}

// AuthorUserIDProvider resolves the CLI's own authenticated principal for
// verifying who queued a remote input, before SessionPool ever forwards one
// through RemoteInputs. A package-level var (like SubagentProgressRegistrar)
// so a test can substitute a fixed identity without a real logged-in session
// or a network call to Whoami.
var AuthorUserIDProvider = chatsync.DefaultAuthorUserIDProvider

// SessionBusRegistrar binds a session's EventBus into the CLI-side
// session-keyed registry (internal/clichat.RegisterSessionBus) so
// emitSubagentProgress - package-level, no session of its own - can
// publish that session's subagent lifecycle events onto it. Nil (the
// zero value) is a safe no-op: a caller that never wires this (a test,
// or a build that never imports internal/cli) simply gets no chatsync
// routing, exactly like an unset SubagentProgressRegistrar produces no
// UI routing. Mirrors SubagentProgressRegistrar's indirection shape so
// internal/uiadapter never imports internal/cli (INV-TUI-29): only
// internal/newtui, which imports both, wires this at startup.
var SessionBusRegistrar func(sessionID string, bus *events.Bus) (release func())

// Threads returns the SubagentThreads registry every pooled Conversation is
// wired to. Callers building the UI (internal/newtui) pass this same
// instance to Screen.SetSubagentThreads so the dialog resolves whichever
// session is currently active, including one reached by /resume or /new.
func (p *SessionPool) Threads() *SubagentThreads {
	return p.threads
}

type fallbackCompleter struct {
	providerName string
}

func (c fallbackCompleter) Name() string { return c.providerName }
func (c fallbackCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", fmt.Errorf("provider %q has no active client: cannot dispatch", c.providerName)
}
func (c fallbackCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", fmt.Errorf("provider %q has no active client: cannot dispatch", c.providerName)
}
func (c fallbackCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, fmt.Errorf("provider %q has no active client: cannot dispatch", c.providerName)
}

func sessionBindingFactory(sess *chat.Session, res *config.Resolved, state *cliagents.AgentSessionState) func(string, string) (chat.ModelBinding, error) {
	return func(providerName, model string) (chat.ModelBinding, error) {
		if providerName == "" && res != nil {
			providerName = res.ProviderName
		}
		if model == "" && res != nil {
			model = res.Model
		}
		binding, err := cliagents.BuildModelBinding(sess, res, ".", providerName, model, state)
		if err == nil {
			return binding, nil
		}
		// If session is loading and requested saved model is not selectable in current config,
		// fallback to building with current configured provider/model or a fallback completer.
		// For an explicit switch (not loading), fail closed and return the error.
		if sess != nil && sess.IsLoading() {
			if res != nil && (providerName != res.ProviderName || model != res.Model) {
				if b, err2 := cliagents.BuildModelBinding(sess, res, ".", res.ProviderName, res.Model, state); err2 == nil {
					return b, nil
				}
			}
			profile, _ := cliagents.ConfiguredProfile(res, providerName, model)
			var comp provider.Completer
			if res != nil && res.ProviderName != "" {
				comp, _ = provider.New(res)
			}
			if comp == nil {
				comp = fallbackCompleter{providerName: providerName}
			}
			return chat.ModelBinding{
				ProviderName:       providerName,
				Model:              model,
				Completer:          comp,
				Profile:            profile,
				PromptBudgetTokens: sess.PromptBudgetFor(profile),
			}, nil
		}
		return chat.ModelBinding{}, err
	}
}

// Session returns the underlying chat.Session for a session ID, or nil if not present.
func (p *SessionPool) Session(id string) *chat.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

// releaseSessionLease is the per-session release hook ReleaseLeases invokes,
// a var so tests can record the visited sessions without arming real context
// heartbeats (the chat package owns that behavior's own tests).
var releaseSessionLease = func(ctx context.Context, sess *chat.Session) {
	sess.ReleaseContextLease(ctx)
}

// ReleaseLeases releases every pooled session's context lease. The TUI's
// return path must call this on shutdown: only the primary startup session
// is released by the chat surface's own defer, so without this every OTHER
// session resumed in the TUI kept a fresh lease behind (40s heartbeat, 2min
// TTL) and the next process's resume was refused with "live in another
// process" until the TTL ran out - the intermittent resume failure users
// read as a broken binary. Sessions registered under several ids (raw and
// canonical) are released once.
func (p *SessionPool) ReleaseLeases(ctx context.Context) {
	p.mu.Lock()
	// Latch BEFORE draining: ReattachSyncAfterLogin holds p.mu only for its
	// snapshot and re-locks per session, so a re-attach in flight while this
	// drains must see the latch in its own per-session critical section.
	p.released.Store(true)
	seen := make(map[*chat.Session]struct{}, len(p.sessions))
	distinct := make([]*chat.Session, 0, len(p.sessions))
	for _, sess := range p.sessions {
		if sess == nil {
			continue
		}
		if _, dup := seen[sess]; dup {
			continue
		}
		seen[sess] = struct{}{}
		distinct = append(distinct, sess)
	}
	// Paired with the owning session's EventBus, not collected alone: Stop
	// only drains SyncSession's own eventCh, not the bus subscription queue
	// feeding it (DC-30, .agents/quality/defect-taxonomy.md). A pooled
	// session that just ran a heavy-volume turn (subagent fan-out, or
	// [sync].stream_assistant = true) can still have events sitting
	// undelivered in that queue when the TUI quits; Flush must run first, on
	// THIS session's own bus, or the tail is silently lost on process exit.
	type pooledSync struct {
		bus *events.Bus
		ss  *chatsync.SyncSession
	}
	syncList := make([]pooledSync, 0, len(p.syncSessions))
	for id, ss := range p.syncSessions {
		if ss == nil {
			continue
		}
		var bus *events.Bus
		if sess := p.sessions[id]; sess != nil {
			bus = sess.EventBus
		}
		syncList = append(syncList, pooledSync{bus: bus, ss: ss})
	}
	p.syncSessions = make(map[string]*chatsync.SyncSession)
	// Drain busReleases alongside the sync-session teardown above: every
	// entry here came from a successful attachSyncLocked, so its lifetime
	// matches syncList's exactly. Missing entries (SessionBusRegistrar was
	// nil at attach time, or a session was never sync-attached at all) are
	// tolerated - the map simply has no release func for that id.
	busReleaseList := make([]func(), 0, len(p.busReleases))
	for _, release := range p.busReleases {
		if release != nil {
			busReleaseList = append(busReleaseList, release)
		}
	}
	p.busReleases = make(map[string]func())
	p.mu.Unlock()
	// Release outside p.mu: ReleaseContextLease must run lock-free (it joins
	// the heartbeat goroutine and issues a store write with its own timeout).
	for _, sess := range distinct {
		releaseSessionLease(ctx, sess)
	}
	for _, ps := range syncList {
		if ps.bus != nil {
			ps.bus.Flush()
		}
		_ = ps.ss.Stop(ctx)
	}
	for _, release := range busReleaseList {
		release()
	}
	if p.watcher != nil {
		p.watcher.Stop(ctx)
	}
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
func NewSessionPool(initialSess *chat.Session, res *config.Resolved, agentState *cliagents.AgentSessionState, toolsOn bool) *SessionPool {
	pool := &SessionPool{
		sessions:     make(map[string]*chat.Session),
		convs:        make(map[string]*Conversation),
		syncSessions: make(map[string]*chatsync.SyncSession),
		busReleases:  make(map[string]func()),
		res:          res,
		agentState:   agentState,
		toolsOn:      toolsOn,
		threads:      NewSubagentThreads(),
		notices:      make(chan uievent.Event, syncNoticeBuffer),
		remoteInputs: make(chan ports.RemoteInputEvent, remoteInputBuffer),
	}
	if initialSess != nil {
		if res != nil {
			initialSess.SetBindingFactory(sessionBindingFactory(initialSess, res, agentState))
		}
		id := initialSess.SessionID
		conv := NewConversation(initialSess)
		conv.SetSubagents(pool.threads)
		pool.sessions[id] = initialSess
		pool.convs[id] = conv
		pool.attachSyncLocked(initialSess)
	}
	return pool
}

// inheritApprovalLocked carries the approval wiring onto a session the pool
// just built.
//
// /new and /resume hand-copy runtime state from an existing pool member -
// tools, event bus, context store, redaction policy - and carried none of the
// approval state. A threat model measured the result: a session started under
// `deny` with a live approver produced, after /new, policy="" and gate=nil.
// The operator's most restrictive setting silently evaporated on a keystroke
// that looks like housekeeping.
//
// The POLICY comes from config, not from the sibling's live value, because the
// live value may be a transient /yolo. A deliberate, temporary loosening of
// one conversation must not become the starting posture of the next one.
//
// The GATE is inherited, because it is the one the UI is actually reading
// from: the approver is constructed once, bound to the first session, and its
// Pending channel is what the prompt renders from. A fresh session with its
// own unattached gate would block on an approver nobody is watching.
//
// The STANDING cache is not inherited but IS created. "Always allow this call"
// is a decision made about one conversation, so carrying it across /new would
// widen it to a conversation the operator has not seen yet - and leaving the
// session with no cache at all makes the affordance dead rather than fresh:
// DecideApproval guards every standing read and write on a non-nil cache, so
// "a always" would be accepted by the prompt and silently forgotten, in every
// conversation after the first.
func inheritApprovalLocked(sess, existing *chat.Session, res *config.Resolved) {
	if sess == nil {
		return
	}
	if existing != nil {
		sess.ApprovalGate = existing.ApprovalGate
	}
	if sess.ApprovalStanding == nil {
		sess.ApprovalStanding = sdkadapter.NewApprovalStanding()
	}
	policy := ""
	if res != nil {
		policy = res.Approvals.ApprovalPolicy()
	}
	if policy == "" && existing != nil {
		policy = existing.BaseApprovalPolicyValue()
	}
	if policy != "" {
		sess.SetBaseApprovalPolicy(policy)
		sess.SetApprovalPolicy(policy)
	}
}

// CreateFresh creates a brand-new session, inheriting runtime state (tools,
// context store, context manager, event bus) from the first existing pool
// member. It does NOT call Load — the session starts empty.
// The new conversation is registered in the pool and returned.
func (p *SessionPool) CreateFresh() (ports.Conversation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.res == nil {
		return nil, fmt.Errorf("no config provided")
	}

	var comp provider.Completer
	if p.res.ProviderName != "" {
		comp, _ = provider.New(p.res)
	}
	sess := chat.NewSession(p.res, comp)
	sess.UseTools = p.toolsOn

	// Inherit runtime state from the first existing session.
	var sibling *chat.Session
	for _, existing := range p.sessions {
		if existing.Tools != nil {
			sess.Tools = existing.Tools
			sess.MaxToolResultChars = existing.MaxToolResultChars
			sess.BatchResultBudgetBytes = existing.BatchResultBudgetBytes
			sess.RefOnlyTools = existing.RefOnlyTools
		}
		if existing.EventBus != nil {
			sess.EventBus = existing.EventBus
		}
		if mgr := existing.ContextManager(); mgr != nil {
			origPrincipal := existing.ContextPrincipal()
			if origPrincipal.IsBound() {
				newPrincipal, err := contextstate.NewPrincipal(origPrincipal.WorkspaceID, sess.SessionID, origPrincipal.SubjectID)
				if err == nil {
					_ = sess.SetContextManager(mgr, newPrincipal, existing.ContextPolicy())
				}
			}
		}
		// Inherited for the same reason every other field above is: a
		// conversation born from the pool must write context under the same
		// privacy rules as its siblings. Omitting it left a fresh conversation
		// running the ZERO policy, so every payload it wrote was recorded
		// hash-only while a sibling wrote the same content with bytes. Those
		// two writes land on one content ref, and the disagreement used to roll
		// back a whole turn as "payload reference is held by different bytes".
		sess.SetContextRedactionPolicy(existing.ContextRedactionPolicy())
		if store := existing.ContextStore(); store != nil {
			_ = sess.SetContextStore(store)
		}
		sibling = existing
		break
	}
	inheritApprovalLocked(sess, sibling, p.res)

	if p.agentState != nil && p.res != nil {
		sess.SetSurfaceWidener(cliagents.NewSurfaceWidener(sess, p.res, p.agentState))
	}
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, p.agentState))

	conv := NewConversation(sess)
	conv.SetSubagents(p.threads)
	id := sess.SessionID
	p.sessions[id] = sess
	p.convs[id] = conv
	p.attachSyncLocked(sess)
	return conv, nil
}

// GetOrCreate retrieves an active conversation or instantiates a new session
// loaded from the persisted session store.
func (p *SessionPool) GetOrCreate(sessionID string) (ports.Conversation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if conv, ok := p.convs[sessionID]; ok {
		return conv, nil
	}

	if p.res == nil {
		return nil, fmt.Errorf("no config provided")
	}
	var comp provider.Completer
	if p.res != nil && p.res.ProviderName != "" {
		comp, _ = provider.New(p.res)
	}
	sess := chat.NewSession(p.res, comp)
	sess.UseTools = p.toolsOn

	// Inherit tools, event bus, and context store from existing session if set
	var sibling *chat.Session
	for _, existing := range p.sessions {
		if existing.Tools != nil {
			sess.Tools = existing.Tools
			sess.MaxToolResultChars = existing.MaxToolResultChars
			sess.BatchResultBudgetBytes = existing.BatchResultBudgetBytes
			sess.RefOnlyTools = existing.RefOnlyTools
		}
		if existing.EventBus != nil {
			sess.EventBus = existing.EventBus
		}
		if mgr := existing.ContextManager(); mgr != nil {
			origPrincipal := existing.ContextPrincipal()
			if origPrincipal.IsBound() {
				newPrincipal, err := contextstate.NewPrincipal(origPrincipal.WorkspaceID, sess.SessionID, origPrincipal.SubjectID)
				if err == nil {
					policy := existing.ContextPolicy()
					_ = sess.SetContextManager(mgr, newPrincipal, policy)
				}
			}
		}
		sess.SetContextRedactionPolicy(existing.ContextRedactionPolicy())
		if store := existing.ContextStore(); store != nil {
			_ = sess.SetContextStore(store)
		}
		sibling = existing
		break
	}
	inheritApprovalLocked(sess, sibling, p.res)

	if p.agentState != nil && p.res != nil {
		sess.SetSurfaceWidener(cliagents.NewSurfaceWidener(sess, p.res, p.agentState))
	}
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, p.agentState))

	if err := sess.Load(sessionID); err != nil {
		return nil, err
	}
	cliagents.RefreshSummarizerAfterModelSwitch(sess, p.res)
	// Same reasoning as the summarizer refresh above: enableSessionContext
	// seeded token-estimate calibration once, before Load published this
	// session's real saved binding. See RefreshCalibrationAfterModelSwitch's
	// doc comment for what a stale seed does to the context gauge.
	sess.RefreshCalibrationAfterModelSwitch(context.Background())

	conv := NewConversation(sess)
	conv.SetSubagents(p.threads)
	p.sessions[sessionID] = sess
	p.convs[sessionID] = conv
	if sess.SessionID != "" && sess.SessionID != sessionID {
		p.sessions[sess.SessionID] = sess
		p.convs[sess.SessionID] = conv
	}
	p.attachSyncLocked(sess)
	return conv, nil
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
			go p.pumpRemoteInputs(id, syncSess.Inputs())
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
	// See the matching comment in internal/clichat/chat_sync.go: the identity
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
			// See the matching comment in internal/clichat/chat_sync.go: both
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
		// never here) into RemoteInputs(). internal/ui/screen/conversation -
		// the screen actually rendering a turn - is the sole caller of
		// conv.Send for a remote instruction, draining it through the exact
		// same awaitSessionEvent path a local send uses. See
		// docs/design/ui-isolation.md: internal/uiadapter is the one bridge
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
		Deliver: func(sessionID string, in chatsync.RemoteInput) {
			p.remoteInputs <- ports.RemoteInputEvent{
				ID:         in.ID,
				Kind:       in.Kind,
				SessionID:  sessionID,
				Body:       in.Body,
				ReceivedAt: in.Received,
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
