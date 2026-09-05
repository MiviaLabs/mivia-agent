package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
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
	var comp provider.Completer
	if p.res.ProviderName != "" {
		comp, _ = provider.New(p.res)
	}
	sess := chat.NewSession(p.res, comp)
	sess.UseTools = p.toolsOn
	boundRoot := ""
	if bind != nil {
		var err error
		boundRoot, err = bind(sess)
		if err != nil {
			return nil, fmt.Errorf("bind fresh session: %w", err)
		}
	}
	p.inheritWorktreeSessionLocked(sess, false)
	if notice := p.adoptWorktreeToolsLocked(sess, toolRootFor(boundRoot, dir)); notice != "" {
		p.lastToolScopeNotice = notice
	}
	entryState := p.forkEntryStateLocked()
	if entryState != nil {
		sess.SetSurfaceWidener(newSurfaceWidenerVar(sess, p.res, entryState))
	}
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, entryState))
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

func (p *SessionPool) GetOrCreateInDir(id string, bind BindFunc, dir string) (outConv ports.Conversation, outErr error) {
	p.mu.Lock()
	if existing, ok := p.convs[id]; ok {
		p.mu.Unlock()
		return existing, nil
	}
	if inFlight, ok := p.resuming[id]; ok {
		p.mu.Unlock()
		<-inFlight.done
		return inFlight.conv, inFlight.err
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
		return nil, fmt.Errorf("no config provided")
	}
	var comp provider.Completer
	if p.res.ProviderName != "" {
		comp, _ = provider.New(p.res)
	}
	sess := chat.NewSession(p.res, comp)
	sess.UseTools = p.toolsOn
	boundRoot := ""
	if bind != nil {
		var err error
		boundRoot, err = bind(sess)
		if err != nil {
			return nil, fmt.Errorf("bind session %q: %w", id, err)
		}
	}
	p.inheritWorktreeSessionLocked(sess, true)
	if notice := p.adoptWorktreeToolsLocked(sess, toolRootFor(boundRoot, dir)); notice != "" {
		p.lastToolScopeNotice = notice
	}
	entryState := p.forkEntryStateLocked()
	if entryState != nil {
		sess.SetSurfaceWidener(newSurfaceWidenerVar(sess, p.res, entryState))
	}
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, entryState))
	if err := sess.Load(id); err != nil {
		return nil, err
	}
	cliagents.RefreshSummarizerAfterModelSwitch(sess, p.res)
	sess.RefreshCalibrationAfterModelSwitch(context.Background())
	if existing := p.liveEntryForResolvedLocked(id, sess); existing != nil {
		return existing, nil
	}
	conv := NewConversation(sess)
	conv.SetSubagents(p.threads)
	p.publishEntryLocked(id, sess, conv, entryState)
	p.lastCreated = conv
	p.attachSyncLocked(sess)
	return conv, nil
}

func (p *SessionPool) inheritWorktreeSessionLocked(sess *chat.Session, withPolicies bool) {
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
			principal := existing.ContextPrincipal()
			if principal.IsBound() {
				if next, err := contextstate.NewPrincipal(principal.WorkspaceID, sess.SessionID, principal.SubjectID); err == nil {
					if withPolicies {
						_ = sess.SetContextManager(mgr, next, existing.ContextPolicy())
					} else {
						_ = sess.SetContextManager(mgr, next)
					}
				}
			}
		}
		sess.SetContextRedactionPolicy(existing.ContextRedactionPolicy())
		if store := existing.ContextStore(); store != nil {
			_ = sess.SetContextStore(store)
		}
		inheritApprovalLocked(sess, existing, p.res)
		break
	}
}

// CloseAll releases memoized worktree registries.
func (p *SessionPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, closeFn := range p.regCloses {
		closeFn()
	}
	p.regCloses = nil
	p.regByRoot = nil
}
