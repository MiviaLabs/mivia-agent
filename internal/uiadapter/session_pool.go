package uiadapter

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// SessionPool manages active and resumed sessions in memory.
// It allows background sessions to keep running while the user switches
// freely between them.
type SessionPool struct {
	mu         sync.Mutex
	sessions   map[string]*chat.Session
	convs      map[string]*Conversation
	res        *config.Resolved
	agentState *cliagents.AgentSessionState
	toolsOn    bool
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
		binding, err := cliagents.BuildModelBinding(sess, res, ".", providerName, model, state)
		if err == nil {
			return binding, nil
		}
		profile, _ := cliagents.ConfiguredProfile(res, providerName, model)
		return chat.ModelBinding{
			ProviderName:       providerName,
			Model:              model,
			Completer:          fallbackCompleter{providerName: providerName},
			Profile:            profile,
			PromptBudgetTokens: sess.PromptBudgetFor(profile),
		}, nil
	}
}

// NewSessionPool constructs a SessionPool seeded with the initial session.
func NewSessionPool(initialSess *chat.Session, res *config.Resolved, agentState *cliagents.AgentSessionState, toolsOn bool) *SessionPool {
	pool := &SessionPool{
		sessions:   make(map[string]*chat.Session),
		convs:      make(map[string]*Conversation),
		res:        res,
		agentState: agentState,
		toolsOn:    toolsOn,
	}
	if initialSess != nil {
		if res != nil {
			initialSess.SetBindingFactory(sessionBindingFactory(initialSess, res, agentState))
		}
		id := initialSess.SessionID
		pool.sessions[id] = initialSess
		pool.convs[id] = NewConversation(initialSess)
	}
	return pool
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
	sess.SetBindingFactory(sessionBindingFactory(sess, p.res, p.agentState))

	// Inherit session directory and session/context stores from existing session if set
	for _, existing := range p.sessions {
		if existing.SessionDir != "" {
			sess.SessionDir = existing.SessionDir
		}
		if store := existing.Store(); store != nil {
			sess.SetSessionStore(store, nil)
		}
		if mgr := existing.ContextManager(); mgr != nil {
			origPrincipal := existing.ContextPrincipal()
			if origPrincipal.IsBound() {
				newPrincipal, err := contextstate.NewPrincipal(origPrincipal.WorkspaceID, sess.SessionID, origPrincipal.SubjectID)
				if err == nil {
					_ = sess.SetContextManager(mgr, newPrincipal)
				}
			}
		}
		if store := existing.ContextStore(); store != nil {
			_ = sess.SetContextStore(store)
		}
		break
	}

	if err := sess.Load(sessionID); err != nil {
		return nil, err
	}

	conv := NewConversation(sess)
	p.sessions[sessionID] = sess
	p.convs[sessionID] = conv
	if sess.SessionID != "" && sess.SessionID != sessionID {
		p.sessions[sess.SessionID] = sess
		p.convs[sess.SessionID] = conv
	}
	return conv, nil
}
