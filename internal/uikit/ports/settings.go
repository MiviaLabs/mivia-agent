package ports

import "context"

// Scope names which config layer a settings write targets: user
// (~/.mivia/mivia.toml) or project (<workspace>/.mivia/mivia.toml).
// internal/config layers these with project overrides winning
// (load.go:361), so a write that does not say which file it lands in
// cannot be applied correctly - Scope is always a call parameter, never
// a field folded into a view or edit value.
type Scope int

const (
	ScopeUser Scope = iota
	ScopeProject
	// ScopeBuiltin marks compiled content shipped inside the mivia binary
	// (the built-in agents). It is appended last so any serialized numeric
	// scope stays stable. Builtin rows are read-only: there is no file
	// behind them to write or remove.
	ScopeBuiltin
)

func (s Scope) String() string {
	if s == ScopeProject {
		return "project"
	}
	if s == ScopeBuiltin {
		return "builtin"
	}
	return "user"
}

// SaveState is where one Apply call's async write has reached.
type SaveState int

const (
	SavePending SaveState = iota
	SaveValidating
	SaveSaved
	SaveFailed
)

// SaveEvent is one step of a save's progress. Message is always
// adapter-authored text naming what happened to a FIELD; it must never
// echo the value that field held, to avoid leaking a secret field's value.
type SaveEvent struct {
	State   SaveState
	Field   string
	Message string
}

// SaveHandle is the async result of one Apply call - the same
// channel-based shape as TurnHandle, so the UI has one convention for
// "something is in flight" rather than a second one for saves.
type SaveHandle interface {
	ID() string
	Events() <-chan SaveEvent
	Cancel()
}

// Settings is the settings screen's whole dependency surface: one
// nil-able field per section, mirroring the nil CommandRunner
// convention (commands.go:38) so a section with no adapter yet renders
// "unavailable" instead of the screen failing to build.
type Settings struct {
	General     GeneralSettings
	Projects    ProjectSettings
	Providers   ProviderSettings
	MCP         MCPSettings
	Agents      AgentSettings
	Automations AutomationSettings
	Skills      SkillSettings
}

// GeneralView is the app-level toggles the General section reads.
type GeneralView struct {
	Theme                  string
	Mouse                  bool
	ShowReasoning          bool
	ShowIterationNotices   bool
	ShowPromptCacheNotices bool
	ScrollLines            int
	ApprovalDefault        string // "once" | "always" | "deny"
	ScreenReader           bool
	ReducedMotion          bool
}

// GeneralEdit is a closed union, one variant per General field: a
// stringly-typed key/value setter would let a typo compile; this
// cannot.
type GeneralEdit interface{ isGeneralEdit() }

type SetTheme struct{ Name string }
type SetMouse struct{ On bool }
type SetShowReasoning struct{ On bool }
type SetShowIterationNotices struct{ On bool }
type SetShowPromptCacheNotices struct{ On bool }
type SetScrollLines struct{ N int }
type SetApprovalDefault struct{ Mode string }
type SetScreenReader struct{ On bool }
type SetReducedMotion struct{ On bool }

func (SetTheme) isGeneralEdit()                  {}
func (SetMouse) isGeneralEdit()                  {}
func (SetShowReasoning) isGeneralEdit()          {}
func (SetShowIterationNotices) isGeneralEdit()   {}
func (SetShowPromptCacheNotices) isGeneralEdit() {}
func (SetScrollLines) isGeneralEdit()            {}
func (SetApprovalDefault) isGeneralEdit()        {}
func (SetScreenReader) isGeneralEdit()           {}
func (SetReducedMotion) isGeneralEdit()          {}

// GeneralSettings is the General section's read/write surface.
type GeneralSettings interface {
	General() GeneralView
	Apply(ctx context.Context, scope Scope, e GeneralEdit) (SaveHandle, error)
}
