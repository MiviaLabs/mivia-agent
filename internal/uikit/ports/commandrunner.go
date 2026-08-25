package ports

import "context"

// CommandOutcome is what a slash command asks the UI to do next. The
// screen checks the fields in a fixed priority order (see
// internal/ui/screen/conversation/commands.go): Err first, then Quit,
// then the open-a-modal fields, then ClearTranscript, then
// ModelChoiceGroups, then AgentChoices, then SessionChoices, then
// Notice. Only one field is normally set per outcome.
type CommandOutcome struct {
	// Err, when non-empty, is shown as a transcript error block instead
	// of any other field. Set this for an unknown command or a command
	// that cannot run right now.
	Err string

	// Quit asks the UI to exit the program.
	Quit bool

	// OpenTheme asks the UI to open the existing theme picker.
	OpenTheme bool

	// OpenSettings asks the UI to push the full-screen settings modal.
	// SettingsSection, if non-empty, deep-links to one section
	// ("general" | "models" | "mcp" | "agents" | "automations",
	// case-insensitive); an unresolved name is the runner's to reject
	// with Err, not the UI's to silently fall back from.
	OpenSettings    bool
	SettingsSection string

	// OpenHelp asks the UI to open the existing keymap overlay.
	OpenHelp bool

	// OpenQueue asks the UI to open the queue manager overlay.
	OpenQueue bool

	// ClearTranscript asks the UI to empty the transcript view.
	ClearTranscript bool

	// ModelChoices, when non-empty, asks the UI to open a picker over
	// these model names, first entry highlighted.
	ModelChoices []string

	// ModelChoiceGroups, when non-empty, asks the UI to open a
	// grouped picker. The provider name is shown as a non-selectable
	// header row; each Models entry is a selectable row. An empty
	// Provider renders its items as a flat list without a header.
	// The chosen model name comes back through
	// CommandRunner.SelectModel.
	ModelChoiceGroups []ModelChoiceGroup

	// AgentChoices, when non-empty, asks the UI to open a picker over
	// these agent names, first entry highlighted. The chosen name comes
	// back through CommandRunner.SelectAgent. The default orchestrator
	// (DefaultAgentName) is always a member of a real roster; the UI
	// does not enforce it, the harness's roster does.
	AgentChoices []string

	// SessionChoices, when non-empty, asks the UI to open a session picker
	// over these session summaries. The chosen session ID comes back through
	// CommandRunner.SelectSession.
	SessionChoices []SessionSummary

	// EffortChoices, when non-empty, asks the UI to open a reasoning effort
	// picker over these effort levels. The chosen level comes back through
	// CommandRunner.SelectEffort.
	EffortChoices []string

	// Notice, when non-empty, is appended to the transcript as an
	// informational block.
	Notice string

	// Conversation, when non-nil, asks the UI to switch the active
	// conversation to this new instance.
	Conversation Conversation
}

// ModelChoiceGroup groups selectable models under one provider for
// the /model picker dialog. Provider is shown as a header row; an
// empty Provider renders the items as a flat list without a header.
// Groups with no Models are dropped before display.
type ModelChoiceGroup struct {
	Provider string
	Models   []string
}

// Command is a slash command candidate for auto-completion.
type Command struct {
	Name string
	Desc string
}

// CommandRunner executes one slash command against session state - a
// fake today, real harness state after the CLI refactor lands. name is
// the command word without the leading "/"; args is the remainder of
// the line, trimmed. This is the integration seam
// docs/design/ui-isolation.md names for slash commands: a screen calls
// it and never inspects harness internals directly.
type CommandRunner interface {
	// Run executes name with args and reports what the UI should do
	// next.
	Run(ctx context.Context, name, args string) CommandOutcome

	// SelectModel applies a model choice returned by a ModelChoices
	// picker and reports a CommandOutcome, typically a confirmation
	// Notice.
	SelectModel(ctx context.Context, name string) CommandOutcome

	// SelectAgent applies an agent choice returned by an AgentChoices
	// picker: switch the session's active agent and report the outcome,
	// typically a confirmation Notice.
	SelectAgent(ctx context.Context, name string) CommandOutcome

	// SelectSession applies a session choice returned by a SessionChoices
	// picker: switch/resume the selected session and report the outcome.
	SelectSession(ctx context.Context, id string) CommandOutcome

	// SelectEffort applies a reasoning effort choice returned by an EffortChoices
	// picker and reports a CommandOutcome, typically a confirmation Notice.
	SelectEffort(ctx context.Context, level string) CommandOutcome
}

// DefaultAgentName is the session's default agent: Mivia, the general
// purpose orchestrator. A roster always carries it; /agents offers it
// so switching back needs no recall of the name.
const DefaultAgentName = "Mivia"
