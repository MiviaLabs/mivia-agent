package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func effortPickerModel(t *testing.T, level reasoning.Level) (*tuiModel, *chat.Session) {
	t.Helper()
	res := pickerEffortConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	if err := sess.SetReasoningEffort(level); err != nil {
		t.Fatal(err)
	}
	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.width, m.height, m.ready = 90, 24, true
	return m, sess
}

func transcript(m *tuiModel) string {
	var b strings.Builder
	for _, block := range m.blocks {
		b.WriteString(stripANSI(block.Text))
		b.WriteString("\n")
	}
	return b.String()
}

// The cursor opens on the active row, so enter is the most likely keystroke in
// this dialog. This also pins the label to the dial across packages: the row's
// annotation is what the user was promised, so whatever the session runs at
// afterwards must be exactly that.
func TestIntegrationModelPickerEnterOnTheActiveRowKeepsTheEffort(t *testing.T) {
	m, sess := effortPickerModel(t, reasoning.Low)
	m.openModelDialog()
	row, ok := m.modelDlg.selected()
	if !ok || row.model != "glm-5.2" {
		t.Fatalf("cursor opened on %+v, want the active row", row)
	}
	advertised := row.effort

	m.handleModelDialogKey("enter")

	if got := string(sess.ReasoningEffort()); got != advertised {
		t.Fatalf("picker advertised effort %q, session runs at %q", advertised, got)
	}
	if got := sess.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q after selecting the active row, want low", got)
	}
	if body := transcript(m); strings.Contains(body, "discarded") {
		t.Fatalf("a preserved effort was announced as discarded:\n%s", body)
	}
}

// Typing the model already in force reaches the same publication with no row
// to read, so it owes the same answer.
func TestIntegrationTypedSameModelKeepsTheEffort(t *testing.T) {
	m, sess := effortPickerModel(t, reasoning.Low)
	if !m.handleSlash("/model glm-5.2") {
		t.Fatal("/model with the active model was not handled")
	}
	if got := sess.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q after retyping the active model, want low", got)
	}
}

// The binding factory is the production path: it resolves a fresh profile
// rather than renaming the published one in place.
func TestIntegrationSameModelThroughTheBindingFactoryKeepsTheEffort(t *testing.T) {
	m, sess := effortPickerModel(t, reasoning.Low)
	profile := pickerEffortConfig().ModelProfiles[0]
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		spec := profile
		spec.Name = model
		return chat.ModelBinding{
			ProviderName: providerName, Model: model,
			Completer: welcomeStubCompleter{}, Profile: spec,
		}, nil
	})
	m.openModelDialog()
	m.handleModelDialogKey("enter")
	if got := sess.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q after republishing through the factory, want low", got)
	}
}

// A real model change still discards the choice - and now says so, because
// silence there is the one case the preservation rule does not cover.
func TestIntegrationModelPickerAnnouncesADiscardedEffort(t *testing.T) {
	m, sess := effortPickerModel(t, reasoning.Low)
	m.openModelDialog()
	rowIndex := -1
	for i, row := range m.modelDlg.rows {
		if row.model == "glm-5.2-air" {
			rowIndex = i
			break
		}
	}
	if rowIndex < 0 {
		t.Fatal("alternative model row missing")
	}
	m.modelDlg.cursor = rowIndex

	m.handleModelDialogKey("enter")

	if got := sess.ReasoningEffort(); got == reasoning.Low {
		t.Fatal("the previous model's effort survived a real model change")
	}
	body := transcript(m)
	if !strings.Contains(body, "low") || !strings.Contains(body, "discarded") {
		t.Fatalf("model change did not report the discarded effort:\n%s", body)
	}
}

// A switch that discards nothing the user chose must stay quiet.
func TestIntegrationModelPickerStaysQuietWhenNoEffortWasChosen(t *testing.T) {
	res := pickerEffortConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.width, m.height, m.ready = 90, 24, true
	m.openModelDialog()
	for i, row := range m.modelDlg.rows {
		if row.model == "glm-4.6" {
			m.modelDlg.cursor = i
			break
		}
	}
	m.handleModelDialogKey("enter")
	if body := transcript(m); strings.Contains(body, "discarded") {
		t.Fatalf("a model default was reported as a discarded choice:\n%s", body)
	}
}
