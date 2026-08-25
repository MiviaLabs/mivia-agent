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
//
// A provider is a SINGLE catalog entry that can be defined (base URL,
// API key env, model list) at either scope, but its default_model is
// the one field that can independently exist at BOTH scopes at once:
// the project file's own [providers.<name>] stanza may set only
// default_model, overriding the user file's value for this workspace
// without redefining anything else about the provider (see
// internal/config.LoadProviderDefaultOverrides). ProviderView therefore
// represents one (provider, scope) row - the same provider name can
// appear once under Global and once under Project when a project
// override exists - and DefaultModel always means "the default_model
// value this row's OWN scope file sets", never a cross-scope merge.
// EffectiveDefault names the model that actually wins at runtime
// (project override if set, else the global value), and is the same
// across every row for this provider so at most one badge in the whole
// section ever reads bare "default".
type ProviderView struct {
	Name           string
	BaseURL        string
	APIKeyEnv      string
	APIKeySet      bool
	Models         []ModelView
	ActiveModel    string
	DefaultModel   string
	Active         bool
	Selectable     bool
	DisabledReason string
	BuiltIn        bool
	// Scope is which config layer this row's own fields were read from:
	// ScopeUser for ~/.mivia/mivia.toml, ScopeProject for the workspace's
	// .mivia/mivia.toml. Mirrors AgentView.Scope/SkillView.Scope.
	Scope Scope
	// HasProjectOverride reports whether the PROJECT file defines its own
	// default_model for this provider, independent of this row's own
	// Scope - a ScopeUser row for a provider WITH a project override
	// still carries HasProjectOverride=true so the UI can badge the
	// global row as "shadowed" instead of silently looking wrong next to
	// a project row claiming the effective default.
	HasProjectOverride bool
	// EffectiveDefaultModel is the model that actually wins at runtime
	// for this provider: the project override when HasProjectOverride,
	// else this provider's global default_model. Identical across every
	// row sharing this provider name.
	EffectiveDefaultModel string
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

// SetDefaultModel sets the persisted default model for a provider in
// config, at the scope passed to Apply (the row's own scope: a Global
// row writes the user file, a Project row writes the project file -
// see providerConfigPathForScope). It never touches the OTHER scope's
// file, so setting a Global default cannot clear an existing project
// override, and vice versa.
type SetDefaultModel struct{ Provider, Model string }

// SetProjectDefaultModel writes a project-scope default_model override
// for Provider regardless of which scope's row it was invoked from
// (unlike SetDefaultModel, whose target scope comes from the row) -
// this is the "make this the default for THIS project" action pressable
// from a Global row. It always targets the project config file.
type SetProjectDefaultModel struct{ Provider, Model string }

// ClearProjectDefaultModel removes Provider's project-scope
// default_model override, reverting it to the provider's global
// default. It leaves the global default_model, and everything else in
// either file, untouched.
type ClearProjectDefaultModel struct{ Provider string }

func (UpsertProvider) isProviderEdit()           {}
func (RemoveProvider) isProviderEdit()           {}
func (UpsertModel) isProviderEdit()              {}
func (RemoveModel) isProviderEdit()              {}
func (ActivateModel) isProviderEdit()            {}
func (SetDefaultModel) isProviderEdit()          {}
func (SetProjectDefaultModel) isProviderEdit()   {}
func (ClearProjectDefaultModel) isProviderEdit() {}

// ProviderSettings is the Models section's read/write surface.
type ProviderSettings interface {
	Providers() []ProviderView
	Apply(ctx context.Context, scope Scope, e ProviderEdit) (SaveHandle, error)
}
