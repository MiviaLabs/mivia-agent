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
	if p.agentState != nil {
		sess.SetSurfaceWidener(cliagents.NewSurfaceWidener(sess, p.res, p.agentState))
	}
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, p.agentState))
	conv := NewConversation(sess)
	conv.SetSubagents(p.threads)
	p.sessions[sess.SessionID] = sess
	p.convs[sess.SessionID] = conv
	p.lastCreated = conv
	p.attachSyncLocked(sess)
	return conv, nil
}

func (p *SessionPool) GetOrCreateBound(id string, bind BindFunc) (ports.Conversation, error) {
	return p.GetOrCreateInDir(id, bind, "")
}

func (p *SessionPool) GetOrCreateInDir(id string, bind BindFunc, dir string) (ports.Conversation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if conv, ok := p.convs[id]; ok {
		return conv, nil
	}
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
	if p.agentState != nil {
		sess.SetSurfaceWidener(cliagents.NewSurfaceWidener(sess, p.res, p.agentState))
	}
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, p.agentState))
	if err := sess.Load(id); err != nil {
		return nil, err
	}
	cliagents.RefreshSummarizerAfterModelSwitch(sess, p.res)
	sess.RefreshCalibrationAfterModelSwitch(context.Background())
	conv := NewConversation(sess)
	conv.SetSubagents(p.threads)
	p.sessions[id] = sess
	p.convs[id] = conv
	if sess.SessionID != "" && sess.SessionID != id {
		p.sessions[sess.SessionID] = sess
		p.convs[sess.SessionID] = conv
	}
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
	p.registriesClosed = true
	for _, closeFn := range p.regCloses {
		closeFn()
	}
	p.regCloses = nil
	p.regByRoot = nil
}
