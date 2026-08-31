package uiadapter

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// SessionPool manages active and resumed sessions in memory.
// It allows background sessions to keep running while the user switches
// freely between them.
type SessionPool struct {
	mu           sync.Mutex
	sessions     map[string]*chat.Session
	convs        map[string]*Conversation
	syncSessions map[string]*chatsync.SyncSession
	res          *config.Resolved
	agentState   *cliagents.AgentSessionState
	toolsOn      bool
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
}

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
	syncList := make([]*chatsync.SyncSession, 0, len(p.syncSessions))
	for _, ss := range p.syncSessions {
		if ss != nil {
			syncList = append(syncList, ss)
		}
	}
	p.syncSessions = make(map[string]*chatsync.SyncSession)
	p.mu.Unlock()
	// Release outside p.mu: ReleaseContextLease must run lock-free (it joins
	// the heartbeat goroutine and issues a store write with its own timeout).
	for _, sess := range distinct {
		releaseSessionLease(ctx, sess)
	}
	for _, ss := range syncList {
		_ = ss.Stop(ctx)
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
		res:          res,
		agentState:   agentState,
		toolsOn:      toolsOn,
		threads:      NewSubagentThreads(),
		notices:      make(chan uievent.Event, syncNoticeBuffer),
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

// CreateFresh creates a brand-new session, inheriting runtime state (tools,
// store, context manager, event bus, session directory) from the first
// existing pool member. It does NOT call Load — the session starts empty.
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
	for _, existing := range p.sessions {
		if existing.SessionDir != "" {
			sess.SessionDir = existing.SessionDir
		}
		if existing.Tools != nil {
			sess.Tools = existing.Tools
			sess.MaxToolResultChars = existing.MaxToolResultChars
			sess.BatchResultBudgetBytes = existing.BatchResultBudgetBytes
			sess.RefOnlyTools = existing.RefOnlyTools
		}
		if existing.EventBus != nil {
			sess.EventBus = existing.EventBus
		}
		if store := existing.Store(); store != nil {
			sess.SetSessionStore(store, nil)
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
		break
	}

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

	// Inherit session directory, tools, event bus, and session/context stores from existing session if set
	for _, existing := range p.sessions {
		if existing.SessionDir != "" {
			sess.SessionDir = existing.SessionDir
		}
		if existing.Tools != nil {
			sess.Tools = existing.Tools
			sess.MaxToolResultChars = existing.MaxToolResultChars
			sess.BatchResultBudgetBytes = existing.BatchResultBudgetBytes
			sess.RefOnlyTools = existing.RefOnlyTools
		}
		if existing.EventBus != nil {
			sess.EventBus = existing.EventBus
		}
		if store := existing.Store(); store != nil {
			sess.SetSessionStore(store, nil)
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
		break
	}

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
	if p.res == nil || sess == nil || sess.EventBus == nil {
		return
	}
	id := sess.SessionID
	if id == "" {
		return
	}
	if _, exists := p.syncSessions[id]; exists {
		return
	}
	tokens := chatsync.DefaultTokenProvider()
	if !p.res.Sync.Active(tokens != nil) {
		return
	}
	opts := poolSyncOptions(sess, id, p.res, tokens)
	// "Stop syncing and SAY SO". pushNotice takes no lock and drops rather
	// than blocks, so it is safe both from here (under p.mu) and from
	// chatsync's detached stop goroutine.
	opts.OnStop = func(reason string) {
		p.pushNotice("chat sync stopped: " + reason)
	}
	syncSess, err := chatsync.OpenSession(context.Background(), sess.EventBus, id, opts)
	if err == nil {
		p.syncSessions[id] = syncSess
		p.pushNotice("chat sync is running")
	}
}

// poolSyncOptions builds the SessionOptions the TUI session pool hands to
// chatsync.OpenSession. It is a separate function so a test can drive the
// exact value production uses, instead of asserting on a hand-built copy that
// can drift away from the wiring it claims to cover.
func poolSyncOptions(sess *chat.Session, id string, res *config.Resolved, tokens chatsync.TokenProvider) chatsync.SessionOptions {
	// See the matching comment in internal/clichat/chat_sync.go: the identity
	// must be resolved before the options, because OutboxDir has to carry the
	// local handle before OpenSession opens the outbox.
	identityDir := chatsync.IdentityDir(sess.SessionDir)
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
		OutboxDir:       chatsync.OutboxDirFor(sess.SessionDir, ident.LocalHandle),
		LocalHandle:     ident.LocalHandle,
		RemoteSessionID: ident.RemoteSessionID,
		Identity:        chatsync.IdentityRef{Dir: identityDir, Key: key},
		MaxUnflushed:    res.Sync.MaxUnflushed,
		PollWaitSeconds: res.Sync.PollWaitSeconds,
		HeartbeatPeriod: config.SaturatingSeconds(res.Sync.HeartbeatSeconds),
		CreateTitle:     "Session",
		// Remote input is DISABLED. The poller fed server-supplied text
		// straight into conv.Send as a local turn, with no confirmation,
		// into a runtime whose approval default auto-approves run_command
		// (internal/config/bootstrap.go). chatsync.InputPoller and
		// RemoteInput stay in place for the S9 approval-port redesign and
		// the web viewer's reply path, but they are unreachable from here
		// until that lands. Do not read the poller as live code.
		EnablePolling: false,
	}
}
