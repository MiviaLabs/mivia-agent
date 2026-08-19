package ports

import "context"

// CommandOutcome is what a slash command asks the UI to do next. The
// screen checks the fields in a fixed priority order (see
// internal/ui/screen/conversation/commands.go): Err first, then Quit,
// then the open-a-modal fields, then ClearTranscript, then
// ModelChoices, then Notice. Only one field is normally set per
// outcome.
type CommandOutcome struct {
	// Err, when non-empty, is shown as a transcript error block instead
	// of any other field. Set this for an unknown command or a command
	// that cannot run right now.
	Err string

	// Quit asks the UI to exit the program.
	Quit bool

	// OpenTheme asks the UI to open the existing theme picker.
	OpenTheme bool

	// OpenHelp asks the UI to open the existing keymap overlay.
	OpenHelp bool

	// ClearTranscript asks the UI to empty the transcript view.
	ClearTranscript bool

	// ModelChoices, when non-empty, asks the UI to open a picker over
	// these model names. The chosen name comes back through
	// CommandRunner.SelectModel.
	ModelChoices []string

	// Notice, when non-empty, is appended to the transcript as an
	// informational block.
	Notice string
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
}
