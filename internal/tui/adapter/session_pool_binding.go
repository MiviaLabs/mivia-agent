package adapter

// Split from session_pool.go for maintainability (move-only, no logic change).

import (
	"context"
	"fmt"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

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

// newSurfaceWidenerVar and buildModelBindingVar are the two host-owned
// closures the runtime invokes INTERNALLY on a pooled session (deferred-tool
// admission and /model rebuilds). They are vars so a test can record which
// *AgentSessionState each pooled session was wired to: that state must be the
// session's own fork, never the pool's shared base (bug-audit "widener and
// binding factory bound to the shared base state").
var (
	newSurfaceWidenerVar = agents.NewSurfaceWidener
	buildModelBindingVar = agents.BuildModelBinding
)

func sessionBindingFactory(sess *chat.Session, res *config.Resolved, state *agents.AgentSessionState) func(string, string) (chat.ModelBinding, error) {
	return func(providerName, model string) (chat.ModelBinding, error) {
		if providerName == "" && res != nil {
			providerName = res.ProviderName
		}
		if model == "" && res != nil {
			model = res.Model
		}
		binding, err := buildModelBindingVar(sess, res, ".", providerName, model, state)
		if err == nil {
			return binding, nil
		}
		// If session is loading and requested saved model is not selectable in current config,
		// fallback to building with current configured provider/model or a fallback completer.
		// For an explicit switch (not loading), fail closed and return the error.
		if sess != nil && sess.IsLoading() {
			if res != nil && (providerName != res.ProviderName || model != res.Model) {
				if b, err2 := buildModelBindingVar(sess, res, ".", res.ProviderName, res.Model, state); err2 == nil {
					return b, nil
				}
			}
			profile, _ := agents.ConfiguredProfile(res, providerName, model)
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
