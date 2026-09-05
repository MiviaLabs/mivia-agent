package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// BindFunc binds a new session and returns the validated worktree root.
type BindFunc func(*chat.Session) (string, error)

func (p *SessionPool) CreateFreshBound(bind BindFunc) (ports.Conversation, error) {
	return p.CreateFreshInDir(bind, "")
}

func (p *SessionPool) CreateFreshInDir(bind BindFunc, dir string) (ports.Conversation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.res == nil {
		return nil, fmt.Errorf("no config provided")
	}
	sess := p.newEntrySessionLocked()
	boundRoot := ""
	if bind != nil {
		var err error
		boundRoot, err = bind(sess)
		if err != nil {
			return nil, fmt.Errorf("bind fresh session: %w", err)
		}
	}
	entryState := p.wireEntryLocked(sess, boundRoot, dir, false)
	if err := p.refuseIfDrainedLocked(); err != nil {
		return nil, err
	}
	conv := NewConversation(sess)
	conv.SetSubagents(p.threads)
	p.sessions[sess.SessionID] = sess
	p.convs[sess.SessionID] = conv
	p.bindEntryStateLocked(sess.SessionID, entryState)
	p.lastCreated = conv
	p.attachSyncLocked(sess)
	return conv, nil
}

func (p *SessionPool) GetOrCreateBound(id string, bind BindFunc) (ports.Conversation, error) {
	return p.GetOrCreateInDir(id, bind, "")
}

// resumeInFlight lets a second GetOrCreateInDir call for the same id join the
// first instead of building its own independent Session/Conversation for
// it. adoptWorktreeToolsLocked releases p.mu around its slow registry build
// (compute-then-adopt), so a racing caller can pass the initial p.convs[id]
// check before the first caller has published - without this, both callers
// built and returned DIFFERENT conversations for the identical id, and
// whichever published last silently won the map slot while the other kept
// serving its orphaned twin.
type resumeInFlight struct {
	done chan struct{}
	conv ports.Conversation
	err  error
}

// GetOrCreateInDir resolves or builds the pooled entry for id. bind is the
// caller's worktree binding when it already has one (the /resume picker
// carries the instance its row promised); a nil bind is resolved from the
// STORE instead, so a bare id reaches the same bound session through this one
// path - the UI's background Mount and the remote/chat-sync route have no
// listing row, and before that resolution they took the plain loader, which
// filters instance_id IS NULL and cannot see a worktree session at all.
func (p *SessionPool) GetOrCreateInDir(id string, bind BindFunc, dir string) (ports.Conversation, error) {
	conv, discarded, keepLease, err := p.getOrCreateInDirLocking(id, bind, dir)
	// Outside p.mu, per ReleaseContextLease's own lock contract: it joins the
	// heartbeat goroutine and issues a store write with its own timeout, so
	// releasing under the pool lock stalls every other pool operation behind
	// it. A session wired to the context store but never published owns a
	// heartbeat only this release stops.
	//
	// keepLease marks the one discard that must NOT write to the store: the
	// twin resolved to a session already live here, which still owns that
	// lease row. See getOrCreateInDirLocking's live-entry branch.
	if discarded != nil {
		if keepLease {
			discarded.StopContextLeaseHeartbeat()
		} else {
			releaseSessionLease(context.Background(), discarded)
		}
	}
	return conv, err
}

func (p *SessionPool) getOrCreateInDirLocking(id string, bind BindFunc, dir string) (outConv ports.Conversation, discarded *chat.Session, keepLease bool, outErr error) {
	p.mu.Lock()
	if existing, ok := p.convs[id]; ok {
		p.mu.Unlock()
		return existing, nil, false, nil
	}
	if inFlight, ok := p.resuming[id]; ok {
		p.mu.Unlock()
		<-inFlight.done
		return inFlight.conv, nil, false, inFlight.err
	}
	if p.resuming == nil {
		p.resuming = make(map[string]*resumeInFlight)
	}
	inFlight := &resumeInFlight{done: make(chan struct{})}
	p.resuming[id] = inFlight
	defer p.mu.Unlock()
	defer func() {
		delete(p.resuming, id)
		inFlight.conv, inFlight.err = outConv, outErr
		close(inFlight.done)
	}()
	if p.res == nil {
		return nil, nil, false, fmt.Errorf("no config provided")
	}
	bind, dir, err := p.resolveEntryRouteLocked(id, bind, dir)
	if err != nil {
		return nil, nil, false, err
	}
	sess := p.newEntrySessionLocked()
	// A session wired to the context store owns a lease heartbeat that only
	// ReleaseContextLease stops, so every exit that does NOT publish sess
	// hands it back to the caller to release outside the lock: a failed Load
	// leaked one goroutine and one renewing lease per attempt, and a lease
	// renewed by a session nobody can reach blocks every later resume of
	// that id.
	published := false
	defer func() {
		if !published {
			discarded = sess
		}
	}()
	boundRoot := ""
	if bind != nil {
		var err error
		boundRoot, err = bind(sess)
		if err != nil {
			return nil, nil, false, fmt.Errorf("bind session %q: %w", id, err)
		}
	}
	entryState := p.wireEntryLocked(sess, boundRoot, dir, true)
	if err := sess.Load(id); err != nil {
		return nil, nil, false, err
	}
	cliagents.RefreshSummarizerAfterModelSwitch(sess, p.res)
	sess.RefreshCalibrationAfterModelSwitch(context.Background())
	if err := p.refuseIfDrainedLocked(); err != nil {
		return nil, nil, false, err
	}
	if existing := p.liveEntryForResolvedLocked(id, sess); existing != nil {
		// keepLease: sess.Load resolved this build onto a session ALREADY
		// live in this pool, so the durable context row now belongs to that
		// live entry. The twin is thrown away, but its heartbeat's release
		// reads the LIVE principal - which Load just rewrote to the resolved
		// id - so the ordinary discard would clear the lease of the session
		// the user is still using: its own RenewLease would then match no
		// row for the rest of the process's life, and any other process
		// could reclaim a conversation that is open on screen. Stop the
		// twin's heartbeat; write nothing.
		return existing, sess, true, nil
	}
	conv := NewConversation(sess)
	conv.SetSubagents(p.threads)
	p.publishEntryLocked(id, sess, conv, entryState)
	published = true
	p.lastCreated = conv
	p.attachSyncLocked(sess)
	return conv, nil, false, nil
}

// newEntrySessionLocked builds the bare session every pooled entry starts
// from. Callers hold p.mu.
func (p *SessionPool) newEntrySessionLocked() *chat.Session {
	var comp provider.Completer
	if p.res.ProviderName != "" {
		comp, _ = provider.New(p.res)
	}
	sess := chat.NewSession(p.res, comp)
	sess.UseTools = p.toolsOn
	return sess
}

// wireEntryLocked applies the shared per-entry wiring: inherited runtime
// state and approval posture, the per-root tool registry for boundRoot/dir,
// and the entry's OWN forked agent state behind the two closures the runtime
// invokes internally (deferred-tool widener, /model binding factory) - which
// must never close over the pool's shared base. Returns that fork so the
// caller registers it under the entry's key. Callers hold p.mu.
func (p *SessionPool) wireEntryLocked(sess *chat.Session, boundRoot, dir string, withPolicies bool) *cliagents.AgentSessionState {
	inheritApprovalLocked(sess, p.inheritEntryStateLocked(sess, withPolicies), p.res)
	if notice := p.adoptWorktreeToolsLocked(sess, toolRootFor(boundRoot, dir)); notice != "" {
		p.lastToolScopeNotice = notice
	}
	entryState := p.forkEntryStateLocked()
	if entryState != nil {
		sess.SetSurfaceWidener(newSurfaceWidenerVar(sess, p.res, entryState))
	}
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, entryState))
	return entryState
}

// contextStoreLocked returns the repository store any pooled member is bound
// to. It deliberately does NOT go through preferredInheritanceSessionLocked,
// which only ever returns a member carrying a tool REGISTRY: the binding is
// recorded in the store, and the store is installed independently of tools,
// so resolving it through the tool-scoped donor made every bare-id resume
// fall back to the plain loader whenever tools were off (--no-tools) - the
// very failure this resolution exists to prevent. Every pooled session
// inherits the same store, so the first bound member is authoritative.
// Callers hold p.mu.
func (p *SessionPool) contextStoreLocked() *storage.SQLite {
	for _, sess := range p.sessions {
		if sess == nil {
			continue
		}
		if store, ok := sess.ContextStore().(*storage.SQLite); ok && store != nil {
			return store
		}
	}
	return nil
}

// storedRouteLocked resolves the managed-worktree route a session id is
// bound to from the store - the authoritative record, so a caller with only
// an id gets the same binding the picker's row would have carried, and a
// plain session gets (nil, "") and the plain path. A bound session whose
// worktree is gone fails closed here rather than resuming detached from the
// checkout it belongs to. Callers hold p.mu.
func (p *SessionPool) storedRouteLocked(id string) (BindFunc, string, error) {
	store := p.contextStoreLocked()
	if store == nil {
		return nil, "", nil
	}
	root, err := worktreeroute.Root("")
	if err != nil {
		return nil, "", nil // not a repository: nothing can be worktree-bound
	}
	principal, err := worktreeroute.Principal(root)
	if err != nil {
		return nil, "", nil
	}
	info, bound, err := store.WorktreeSessionBinding(context.Background(), principal, id)
	if err != nil {
		return nil, "", fmt.Errorf("resolve worktree binding for session %q: %w", id, err)
	}
	if !bound {
		return nil, "", nil
	}
	route := worktreeroute.Route{Worktree: info.Instance.Worktree, Dir: info.CanonicalPath, Instance: info.Instance}
	return worktreeBindFunc(store, root, route), info.CanonicalPath, nil
}

// refuseIfDrainedLocked stops a build that finished after the pool was
// drained from publishing. wireEntryLocked releases p.mu around its registry
// build, so ReleaseLeases can latch and drain inside that window; a session
// published afterwards was never in the snapshot it drained, so nothing ever
// stops its lease heartbeat and the next process's resume of that id is
// refused as "live in another process" for the full TTL. Callers hold p.mu.
func (p *SessionPool) refuseIfDrainedLocked() error {
	if p.released.Load() {
		return fmt.Errorf("session pool is shutting down")
	}
	return nil
}

// CloseAll releases memoized worktree registries.
func (p *SessionPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseWorkflowWatchesLocked()
	for _, closeFn := range p.regCloses {
		closeFn()
	}
	p.regCloses = nil
	p.regByRoot = nil
}

// resolveEntryRouteLocked fills in the managed-worktree route for a caller
// that supplied none, from the STORE - the authoritative record.
//
// Only a RESOLVED route replaces the caller's dir: an unbound session must
// keep the directory the caller asked for, or the per-root tool build is
// skipped entirely. Callers hold p.mu.
func (p *SessionPool) resolveEntryRouteLocked(id string, bind BindFunc, dir string) (BindFunc, string, error) {
	if bind != nil {
		return bind, dir, nil
	}
	storedBind, storedDir, err := p.storedRouteLocked(id)
	if err != nil {
		return nil, "", err
	}
	if storedBind == nil {
		return nil, dir, nil
	}
	return storedBind, storedDir, nil
}
