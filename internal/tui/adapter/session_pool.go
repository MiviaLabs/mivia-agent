package adapter

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/uievent"
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
