package cli

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSuggestFiltersRanksAndAcceptsTrailingSpace(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/lo")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	if !m.suggest.open || len(m.suggest.commands) == 0 || m.suggest.commands[0].Name != "/load" {
		t.Fatalf("suggestions = %#v", m.suggest)
	}
	if skipText, skipView, _ := m.handleSuggestKey("tab"); !skipText || !skipView {
		t.Fatalf("tab return = (%v, %v), want consumed", skipText, skipView)
	}
	if got := m.textarea.Value(); got != "/load " {
		t.Fatalf("accepted value = %q, want trailing-space command", got)
	}
	if m.suggest.open {
		t.Fatal("suggestions remained open after accept")
	}
}

func TestSuggestWorksInWelcomeComposer(t *testing.T) {
	m := newTUIModel(makeTestSession(), nil, true)
	m.ready = true
	m.width = 80
	m.height = 24
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.suggest.open || len(m.suggest.commands) < 2 {
		t.Fatalf("welcome suggestion state = %#v", m.suggest)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.suggest.selected != 1 {
		t.Fatalf("welcome down did not navigate suggestions: %#v", m.suggest)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "/help") {
		t.Fatalf("welcome suggestion popup missing from view:\n%s", view)
	}
}

func TestSuggestWelcomeEnterUsesWelcomeTransition(t *testing.T) {
	m := newTUIModel(makeTestSession(), nil, true)
	m.ready = true
	m.width = 80
	m.height = 24
	m.textarea.SetValue("/he")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	if _, _, _ = m.handleSuggestKey("enter"); m.mode != modeChat || m.overlay == nil {
		t.Fatalf("welcome autocomplete Enter mode=%v overlay=%v", m.mode, m.overlay != nil)
	}
}

func TestSuggestEscStaysDismissedUntilTokenChanges(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/lo")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	if !m.suggest.open {
		t.Fatal("precondition: popup open")
	}
	m.handleSuggestKey("esc")
	m.syncSuggest()
	if m.suggest.open {
		t.Fatal("dismissed popup reopened without a token change")
	}
	m.textarea.SetValue("/loa")
	m.textarea.SetCursor(4)
	m.syncSuggest()
	if !m.suggest.open {
		t.Fatal("popup did not reopen after token changed")
	}
}

func TestSuggestEscDismissalClearsAfterTriggerIsRemoved(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/lo")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	m.handleSuggestKey("esc")
	m.textarea.SetValue("")
	m.textarea.SetCursor(0)
	m.syncSuggest()
	m.textarea.SetValue("/lo")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	if !m.suggest.open {
		t.Fatal("popup remained dismissed after its trigger was removed and re-entered")
	}
}

func TestSuggestNavigationKeysRemainDismissedThroughTextareaUpdate(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyHome, tea.KeyEnd} {
		m := newReadyChatModel(24, 80)
		m.textarea.SetValue("/lo")
		m.textarea.SetCursor(3)
		m.syncSuggest()
		if !m.suggest.open {
			t.Fatal("precondition: popup open")
		}
		m.Update(tea.KeyMsg{Type: key})
		if m.suggest.open {
			t.Fatalf("%v reopened suggestion after textarea navigation", key)
		}
	}
}

func TestSuggestDoesNotTriggerMidLineOrMultiline(t *testing.T) {
	for _, value := range []string{"hello /lo", "/lo\nnext"} {
		m := newReadyChatModel(24, 80)
		m.textarea.SetValue(value)
		m.textarea.SetCursor(len([]rune(value)))
		m.syncSuggest()
		if m.suggest.open {
			t.Fatalf("popup opened for invalid trigger %q", value)
		}
	}
}

func TestApplyTokenReplacePreservesPostTokenText(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/lo trailing")
	applyTokenReplace(&m.textarea, 0, 3, "/load ")
	if got := m.textarea.Value(); got != "/load  trailing" {
		t.Fatalf("replacement = %q", got)
	}
	m.textarea.SetValue("/lo\nnext")
	applyTokenReplace(&m.textarea, 0, 3, "/load ")
	if got := m.textarea.Value(); got != "/load \nnext" {
		t.Fatalf("replacement with newline = %q", got)
	}
}

func TestSuggestNavigationDoesNotResetSelection(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/")
	m.textarea.SetCursor(1)
	m.syncSuggest()
	if len(m.suggest.commands) < 3 {
		t.Fatalf("precondition: only %d suggestions", len(m.suggest.commands))
	}
	m.handleSuggestKey("down")
	m.syncSuggest()
	m.handleSuggestKey("down")
	m.syncSuggest()
	if m.suggest.selected != 2 {
		t.Fatalf("selected = %d, want 2", m.suggest.selected)
	}
}

func TestSuggestPopupRendersAboveComposerWithoutGrowingCanvas(t *testing.T) {
	m := newReadyChatModel(8, 40)
	m.textarea.SetValue("/")
	m.textarea.SetCursor(1)
	m.syncSuggest()
	view := m.View()
	if got := viewLineCount(view); got > 8 {
		t.Fatalf("popup grew canvas to %d lines:\n%s", got, stripANSI(view))
	}
	plain := stripANSI(view)
	if !strings.Contains(plain, "/help") || !strings.Contains(plain, "❯") {
		t.Fatalf("popup or composer missing:\n%s", plain)
	}
}

func TestSuggestPopupStaysInsideChatPaneWithSessionsSidebar(t *testing.T) {
	m := newReadyChatModel(24, 100)
	m.sessionsSidebar = newSessionsSidebar()
	m.textarea.SetValue("/")
	m.textarea.SetCursor(1)
	m.syncSuggest()

	pane := newChatPaneLayout(m.width, true)
	panel, size := renderSuggestPanel(m.suggest, pane.chatWidth, max(0, m.suggestComposerTop()-1))
	got := suggestOverlayRect(m, panel, size)
	if got.x < pane.chatX || got.x+got.w > pane.chatX+pane.chatWidth {
		t.Fatalf("suggestion rect %#v is outside chat pane %#v", got, pane)
	}
}

func TestSuggestAcceptRefreshesSpanAfterLeadingWhitespaceEdit(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/lo")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	m.textarea.SetValue(" /lo")
	m.textarea.SetCursor(4)
	m.syncSuggest()
	m.handleSuggestKey("tab")
	if got := m.textarea.Value(); got != " /load " {
		t.Fatalf("accepted value = %q, want exact current token replacement", got)
	}
}

func TestSuggestEnterWithExistingArgsAcceptsButDoesNotExecute(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/he extra")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	m.handleSuggestKey("enter")
	if m.overlay != nil || m.textarea.Value() != "/help  extra" {
		t.Fatalf("existing args must prevent execution: overlay=%v value=%q", m.overlay != nil, m.textarea.Value())
	}
}

func TestSuggestDismissalDoesNotLeaveStalePopupWhenQueryReturns(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/lo")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	m.handleSuggestKey("esc")
	m.textarea.SetValue("/loa")
	m.textarea.SetCursor(4)
	m.syncSuggest()
	m.textarea.SetValue("/lo")
	m.textarea.SetCursor(3)
	m.syncSuggest()
	if !m.suggest.open || m.suggest.token != "/lo" || m.suggest.commands[0].Name != "/load" {
		t.Fatalf("stale suggestion state: %#v", m.suggest)
	}
}

func TestSuggestEnterRunsOnlyAutoExecuteBuiltins(t *testing.T) {
	t.Run("help runs", func(t *testing.T) {
		m := newReadyChatModel(24, 80)
		m.textarea.SetValue("/he")
		m.textarea.SetCursor(3)
		m.syncSuggest()
		m.handleSuggestKey("enter")
		if m.overlay == nil || m.textarea.Value() != "" {
			t.Fatalf("help enter: overlay=%v value=%q", m.overlay != nil, m.textarea.Value())
		}
	})
	t.Run("model inserts", func(t *testing.T) {
		m := newReadyChatModel(24, 80)
		m.textarea.SetValue("/mo")
		m.textarea.SetCursor(3)
		m.syncSuggest()
		m.handleSuggestKey("enter")
		if m.modelDlg != nil || m.textarea.Value() != "/model " {
			t.Fatalf("model enter: dialog=%v value=%q", m.modelDlg != nil, m.textarea.Value())
		}
	})
}

func TestSkillSlashTurnSendsInstructionsButDisplaysOnlyLabel(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.session.SessionDir = t.TempDir()
	comp := &providerModelCapture{provider: "test", model: "one"}
	m.session.Completer = comp
	registry := skills.NewRegistry()
	if err := registry.Register(skills.Definition{
		Name: "bug-audit", Description: "Audit", Origin: skills.OriginProject, UserInvocable: true,
		Instructions: "SECRET-LIKE-SKILL-BODY",
	}); err != nil {
		t.Fatal(err)
	}
	m.session.SetBindingSkillRegistry(registry)
	m.textarea.SetValue("/bug-audit internal/cli")
	if _, _, _ = m.handleChatEnter(false); !m.waiting {
		t.Fatal("skill slash did not start a normal chat turn")
	}
	m.workerWG.Wait()
	if len(comp.requests) != 1 {
		t.Fatalf("provider requests = %d", len(comp.requests))
	}
	sent := comp.requests[0].Messages[len(comp.requests[0].Messages)-1].Content
	for _, want := range []string{skillTurnPreamble, "SECRET-LIKE-SKILL-BODY", "internal/cli"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("sent prompt missing %q: %q", want, sent)
		}
	}
	for _, block := range m.blocks {
		if strings.Contains(block.Text, "SECRET-LIKE-SKILL-BODY") {
			t.Fatalf("skill body leaked into display block: %#v", block)
		}
	}
	for _, message := range m.session.MessagesCopy() {
		if strings.Contains(message.Content, "SECRET-LIKE-SKILL-BODY") {
			t.Fatalf("skill body leaked into persisted history: %#v", message)
		}
	}
	for _, block := range HydrateChatBlocksForView(m.session.MessagesCopy()) {
		if strings.Contains(block.Text, "SECRET-LIKE-SKILL-BODY") {
			t.Fatalf("skill body leaked into rehydrated display: %#v", block)
		}
	}
	saved, err := m.session.ListSessions()
	if err != nil || len(saved) != 1 {
		t.Fatalf("saved skill session = %v err=%v", saved, err)
	}
	if err := m.session.Load(saved[0].Name); err != nil {
		t.Fatal(err)
	}
	for _, message := range m.session.MessagesCopy() {
		if strings.Contains(message.Content, "SECRET-LIKE-SKILL-BODY") {
			t.Fatalf("skill body leaked into reloaded session: %#v", message)
		}
	}
	if got := m.blocks[len(m.blocks)-1].Text; !strings.Contains(got, "⚙ /bug-audit internal/cli") {
		t.Fatalf("display label = %q", got)
	}
}

func TestSkillSlashTurnWithLeadingWhitespaceDoesNotRepeatCommandInArguments(t *testing.T) {
	m := newReadyChatModel(24, 80)
	registry := skills.NewRegistry()
	if err := registry.Register(skills.Definition{
		Name: "bug-audit", Origin: skills.OriginProject, UserInvocable: true,
		Instructions: "body",
	}); err != nil {
		t.Fatal(err)
	}
	m.session.SetBindingSkillRegistry(registry)
	sent, _, ok := m.skillSlashTurn("  /bug-audit internal/cli")
	if !ok || !strings.Contains(sent, "Arguments:\ninternal/cli") || strings.Contains(sent, "Arguments:\n/bug-audit") {
		t.Fatalf("sent skill turn = %q", sent)
	}
}

func TestSkillSlashQueuesWithoutCancellingCurrentTurn(t *testing.T) {
	m := newReadyChatModel(24, 80)
	registry := skills.NewRegistry()
	if err := registry.Register(skills.Definition{
		Name: "bug-audit", Origin: skills.OriginProject, UserInvocable: true,
		Instructions: "body",
	}); err != nil {
		t.Fatal(err)
	}
	m.session.SetBindingSkillRegistry(registry)
	m.waiting = true
	m.textarea.SetValue("/bug-audit internal/cli")
	m.handleChatEnter(false)
	if got := len(m.pendingQueue); got != 1 {
		t.Fatalf("queued turns = %d, want 1", got)
	}
	if len(m.pendingQueueLabels) != 1 || !strings.Contains(m.pendingQueueLabels[0], "/bug-audit") {
		t.Fatalf("queued display = %v", m.pendingQueueLabels)
	}
	if !m.waiting {
		t.Fatal("queued skill changed active turn state")
	}
}

func TestUnknownSlashDoesNotStartTurn(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.textarea.SetValue("/not-a-command")
	m.handleChatEnter(false)
	if m.waiting {
		t.Fatal("unknown slash started a model turn")
	}
	for _, block := range m.blocks {
		if block.Kind == ChatBlockUser {
			t.Fatalf("unknown slash appended a user turn: %#v", m.blocks)
		}
	}
}

func TestSuggestRetainsAllLargeCatalogCandidatesAndRendersSelectedWindow(t *testing.T) {
	commands := make([]SlashCommand, 300)
	for i := range commands {
		commands[i].Name = "/skill-" + strconv.Itoa(i)
		commands[i].Kind = slashKindSkill
	}
	state := suggestState{open: true, commands: commands, selected: 299}
	panel, _ := renderSuggestPanel(state, 80, 10)
	plain := stripANSI(panel)
	if !strings.Contains(plain, "/skill-299") || !strings.Contains(plain, "(300)") {
		t.Fatalf("selected large-catalog window missing expected command:\n%s", plain)
	}
}
