package conversation

import (
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/uievent"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/composer"
)

// SetCommands supplies the slash-completion candidates. The command set
// belongs to the harness, so the screen takes it rather than inventing
// one.
func (s *Screen) SetCommands(cmds []composer.Command) {
	s.commands = cmds
	s.composer.SetCommands(cmds)
	if s.thread != nil {
		s.thread.SetCommands(cmds)
	}
}

// SetMentions supplies the @-mention candidates for the workspace file
// picker. The caller (harness or demo) builds this list from the workspace
// index; the screen holds no filesystem access.
func (s *Screen) SetMentions(mentions []composer.Mention) {
	s.mentions = mentions
	s.composer.SetMentions(mentions)
	if s.thread != nil {
		s.thread.SetMentions(mentions)
	}
}

// SetCommandRunner supplies the seam a slash command acts through (see
// commands.go). It is the integration knob docs/design/ui-isolation.md
// names for slash commands: the screen never inspects harness state
// directly, only this interface. A nil runner (the zero-value default)
// makes every "/x" line report an error instead of falling through to
// Send.
func (s *Screen) SetCommandRunner(r ports.CommandRunner) {
	s.runner = r
	if s.thread != nil {
		s.thread.SetCommandRunner(r)
	}
}

// SetSubagentThreads supplies the seam the activity panel's thread
// dialog resolves a subagent's conversation through (see thread.go).
// The same integration-knob shape SetCommandRunner uses: nil (the
// zero-value default) makes every subagent entry fall back to the
// read-only step-log view.
func (s *Screen) SetSubagentThreads(t ports.SubagentThreads) { s.threads = t }

// SetSettings supplies the /settings screen's dependency knob. Every
// field of store may itself be nil; the zero value (the default before
// this is ever called) still opens the screen with every section
// reading "unavailable".
func (s *Screen) SetSettings(store ports.Settings) { s.settings = store }

// SetRemoteInputs supplies the inbound steering channel (ports.RemoteInputs).
// Must be called before Init runs (buildApp wires it right after
// construction, alongside SetSubagentThreads); Init arms the one read loop
// that lives for the screen's whole life. nil (the default) means this
// screen never receives remote-origin turns - see remote_input.go.
func (s *Screen) SetRemoteInputs(ch <-chan ports.RemoteInputEvent) { s.remoteInputs = ch }

// SetSessionMounter supplies the session mounter for background remote steering.
func (s *Screen) SetSessionMounter(m ports.SessionMounter) { s.mounter = m }

// SetHideComposer toggles visibility of the composer. When true, the
// composer is omitted from layout and rendering (e.g. for subagent
// history inspection).
func (s *Screen) SetHideComposer(hide bool) {
	s.hideComposer = hide
	if hide {
		s.composer.Blur()
	}
	s.resize()
}

// ObserveAgent records a subagent progress update in the activity panel.
func (s *Screen) ObserveAgent(id string, pr *uievent.Progress) {
	s.panel.observeAgent(id, pr)
}

// SetMouseOverrideHint records the terminal's mouse-override key
// (rule 6.5), shown in the help overlay. Empty clears it.
func (s *Screen) SetMouseOverrideHint(hint string) { s.mouseHint = hint }

// Notice pushes one permanent notice block into the transcript. Startup
// hazard warnings land here: they are part of the conversation record,
// not transient chrome, and they must appear exactly once.
func (s *Screen) Notice(text string) {
	next, _ := s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: text},
	})
	s.transcript = next
}
