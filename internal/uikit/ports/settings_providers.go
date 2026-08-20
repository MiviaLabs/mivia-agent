package ports

import "context"

// ModelView is one model entry under a provider. Field set copied
// verbatim from internal/cli/config_cmd.go's modelSpecJSON, minus
// ReasoningDialect: that comment names it "an internal wire-shape
// detail, not something a model picker needs," and a settings screen
// needs it even less than a CLI picker does.
type ModelView struct {
	Name                string
	ContextWindowTokens int
	MaxOutputTokens     int
	ReasoningEfforts    []string
	Reasoning           string
}

// ProviderView is one configured BYOK provider. APIKeySet is a
// boolean, never the key: internal/config's ProviderRuntime.APIKey is
// documented "must never be rendered," and this type has no field that
// could hold it, so leaking one here is a compile error, not a review
// catch.
type ProviderView struct {
	Name           string
	BaseURL        string
	APIKeyEnv      string
	APIKeySet      bool
	Models         []ModelView
	ActiveModel    string
	Active         bool
	Selectable     bool
	DisabledReason string
	BuiltIn        bool
}

// ProviderEdit is a closed union of provider/model mutations.
type ProviderEdit interface{ isProviderEdit() }

type UpsertProvider struct{ Provider ProviderView }
type RemoveProvider struct{ Name string }
type UpsertModel struct {
	Provider string
	Model    ModelView
}
type RemoveModel struct{ Provider, Model string }

// ActivateModel selects a provider+model as active. For a provider
// different from the one currently running, this takes effect on the
// next start: internal/cli's "/provider" command is read-only today
// ("restart with --provider to switch"); model-within-provider is the
// only part that is live. The section renders that caveat; the port
// does not pretend the switch is instant.
type ActivateModel struct{ Provider, Model string }

func (UpsertProvider) isProviderEdit() {}
func (RemoveProvider) isProviderEdit() {}
func (UpsertModel) isProviderEdit()    {}
func (RemoveModel) isProviderEdit()    {}
func (ActivateModel) isProviderEdit()  {}

// ProviderSettings is the Models section's read/write surface.
type ProviderSettings interface {
	Providers() []ProviderView
	Apply(ctx context.Context, scope Scope, e ProviderEdit) (SaveHandle, error)
}
