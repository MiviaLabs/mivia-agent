package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// installPickerBindingFactory wires the shape the production factory has: each
// switch resolves a FRESH profile for the requested model rather than renaming
// the published one, which is what makes the outgoing model's /effort choice
// disappear. The in-place rename branch is a test-config artifact, so a
// discard test that took it would not be measuring the shipped path.
func installPickerBindingFactory(sess *chat.Session) {
	profiles := pickerEffortConfig().ModelProfiles
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		for _, spec := range profiles {
			if spec.Name == model {
				return chat.ModelBinding{
					ProviderName: providerName, Model: model,
					Completer: welcomeStubCompleter{}, Profile: spec,
				}, nil
			}
		}
		return chat.ModelBinding{}, fmt.Errorf("model is not configured")
	})
}

func discardPickerModel(t *testing.T, level reasoning.Level) (*tuiModel, *chat.Session) {
	t.Helper()
	m, sess := effortPickerModel(t, level)
	installPickerBindingFactory(sess)
	return m, sess
}

func replSession(t *testing.T, level reasoning.Level) (*chat.Session, *config.Resolved, *strings.Builder, *Terminal) {
	t.Helper()
	res := pickerEffortConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	if err := sess.SetReasoningEffort(level); err != nil {
		t.Fatal(err)
	}
	installPickerBindingFactory(sess)
	out := new(strings.Builder)
	return sess, res, out, &Terminal{out: out}
}

// The picker already reported the loss; this pins it against the production
// factory so the seam that computes it, not the call site, is what is measured.
func TestIntegrationPickerReportsDiscardedEffort(t *testing.T) {
	m, sess := discardPickerModel(t, reasoning.Low)
	m.openModelDialog()
	for i, row := range m.modelDlg.rows {
		if row.model == "glm-5.2-air" {
			m.modelDlg.cursor = i
			break
		}
	}
	m.handleModelDialogKey("enter")

	if got := sess.ReasoningEffort(); got == reasoning.Low {
		t.Fatal("the outgoing model's effort survived the switch")
	}
	body := transcript(m)
	if !strings.Contains(body, "effort low discarded") {
		t.Fatalf("picker did not report the discarded effort:\n%s", body)
	}
}

// Typing the switch loses exactly the same dial, so it owes the same account.
func TestIntegrationTypedModelSlashReportsDiscardedEffort(t *testing.T) {
	m, sess := discardPickerModel(t, reasoning.Low)
	if !m.handleSlash("/model glm-5.2-air") {
		t.Fatal("/model with an argument was not handled")
	}
	if got := sess.ReasoningEffort(); got == reasoning.Low {
		t.Fatal("the outgoing model's effort survived the switch")
	}
	body := transcript(m)
	if !strings.Contains(body, "effort low discarded") {
		t.Fatalf("typed /model did not report the discarded effort:\n%s", body)
	}
}

// The plain REPL has no picker and no dialog footer, so its one printed line is
// the only place the loss can be witnessed at all.
func TestIntegrationPlainModelSlashReportsDiscardedEffort(t *testing.T) {
	sess, res, out, term := replSession(t, reasoning.Low)
	if _, _, err := handleSlash("/model glm-5.2-air", sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningEffort(); got == reasoning.Low {
		t.Fatal("the outgoing model's effort survived the switch")
	}
	if !strings.Contains(out.String(), "effort low discarded") {
		t.Fatalf("plain /model did not report the discarded effort:\n%s", out.String())
	}
}

// Republishing the SAME model preserves the override, so announcing a discard
// there would report a loss that did not happen.
func TestIntegrationSameModelSwitchReportsNoDiscardOnAnySurface(t *testing.T) {
	m, sess := discardPickerModel(t, reasoning.Low)
	if !m.handleSlash("/model glm-5.2") {
		t.Fatal("/model with the active model was not handled")
	}
	if got := sess.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q after retyping the active model, want low", got)
	}
	if body := transcript(m); strings.Contains(body, "discarded") {
		t.Fatalf("a preserved effort was announced as discarded:\n%s", body)
	}

	sess2, res, out, term := replSession(t, reasoning.Low)
	if _, _, err := handleSlash("/model glm-5.2", sess2, res, false, term); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "discarded") {
		t.Fatalf("plain surface announced a discard for the active model:\n%s", out.String())
	}
}

// A refused switch changes nothing, so the dial the user set is still in force
// and there is nothing to mourn.
func TestIntegrationFailedModelSwitchReportsNoDiscard(t *testing.T) {
	m, sess := discardPickerModel(t, reasoning.Low)
	if !m.handleSlash("/model glm-nonexistent") {
		t.Fatal("/model with an unknown model was not handled")
	}
	if got := sess.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q after a refused switch, want low", got)
	}
	if body := transcript(m); strings.Contains(body, "discarded") {
		t.Fatalf("a refused switch announced a discard:\n%s", body)
	}

	sess2, res, out, term := replSession(t, reasoning.Low)
	if _, _, err := handleSlash("/model glm-nonexistent", sess2, res, false, term); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "discarded") {
		t.Fatalf("plain surface announced a discard for a refused switch:\n%s", out.String())
	}
}

// A cross-model switch where only the DEFAULT differs took nothing from the
// user: they never chose a level, and the new model is describing itself.
func TestIntegrationDefaultOnlyChangeReportsNoDiscard(t *testing.T) {
	res := pickerEffortConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	installPickerBindingFactory(sess)
	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.width, m.height, m.ready = 90, 24, true
	if !m.handleSlash("/model glm-5.2-air") {
		t.Fatal("/model with an argument was not handled")
	}
	if body := transcript(m); strings.Contains(body, "discarded") {
		t.Fatalf("a model default change was reported as a discarded choice:\n%s", body)
	}

	res2 := pickerEffortConfig()
	sess2 := chat.NewSession(res2, welcomeStubCompleter{})
	installPickerBindingFactory(sess2)
	out := new(strings.Builder)
	if _, _, err := handleSlash("/model glm-5.2-air", sess2, res2, false, &Terminal{out: out}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "discarded") {
		t.Fatalf("plain surface reported a model default change as a discard:\n%s", out.String())
	}
}

// A refused switch must leave the picker open with the reason and must not
// claim a model was set, because the transcript is the only record the user
// keeps of what actually happened.
func TestIntegrationPickerKeepsTheDialogOpenWhenTheSwitchFails(t *testing.T) {
	res := effortCatalogConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	sess.SetBindingFactory(func(string, string) (chat.ModelBinding, error) {
		return chat.ModelBinding{}, errors.New("provider credentials unavailable")
	})
	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.width, m.height = 90, 24
	m.handleSlash("/model")
	if m.modelDlg == nil {
		t.Fatal("/model did not open a dialog")
	}
	row, ok := m.modelDlg.selected()
	if !ok {
		t.Fatal("no row selected")
	}
	m.selectModelDialogRow(row)
	if m.modelDlg == nil {
		t.Fatal("a failed switch must keep the dialog open")
	}
	if m.modelDlg.notice == "" {
		t.Fatal("a failed switch must explain itself")
	}
	for _, block := range m.blocks {
		if strings.Contains(block.Text, "model set to") {
			t.Fatalf("a failed switch announced a model change: %q", block.Text)
		}
	}
}

// The picker opens with the cursor on the row labelled (default), so Enter is
// the shortest way to record a choice that happens to equal the outgoing
// model's default. Levels alone cannot tell that apart from an untouched dial,
// and the user who pressed Enter still loses what they picked.
func TestIntegrationPickerReportsADiscardWhenTheChoiceMatchedTheDefault(t *testing.T) {
	m, sess := discardPickerModel(t, reasoning.High)
	m.openModelDialog()
	for i, row := range m.modelDlg.rows {
		if row.model == "glm-5.2-air" {
			m.modelDlg.cursor = i
			break
		}
	}
	m.handleModelDialogKey("enter")

	if got := sess.ReasoningEffort(); got == reasoning.High {
		t.Fatal("the outgoing model's effort survived the switch")
	}
	body := transcript(m)
	if !strings.Contains(body, "effort high discarded") {
		t.Fatalf("picker dropped a choice that matched the default in silence:\n%s", body)
	}
}

func TestIntegrationTypedModelSlashReportsADiscardWhenTheChoiceMatchedTheDefault(t *testing.T) {
	m, sess := discardPickerModel(t, reasoning.High)
	if !m.handleSlash("/model glm-5.2-air") {
		t.Fatal("/model with an argument was not handled")
	}
	if got := sess.ReasoningEffort(); got == reasoning.High {
		t.Fatal("the outgoing model's effort survived the switch")
	}
	body := transcript(m)
	if !strings.Contains(body, "effort high discarded") {
		t.Fatalf("typed /model dropped a choice that matched the default in silence:\n%s", body)
	}
}

func TestIntegrationPlainModelSlashReportsADiscardWhenTheChoiceMatchedTheDefault(t *testing.T) {
	sess, res, out, term := replSession(t, reasoning.High)
	if _, _, err := handleSlash("/model glm-5.2-air", sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningEffort(); got == reasoning.High {
		t.Fatal("the outgoing model's effort survived the switch")
	}
	if !strings.Contains(out.String(), "effort high discarded") {
		t.Fatalf("plain /model dropped a choice that matched the default in silence:\n%s", out.String())
	}
}

// A choice the incoming model declares as its own default changes nothing the
// user can observe, so naming it a discard would be noise even though the
// override itself was dropped.
func TestIntegrationDiscardIsSilentWhenTheNewDefaultMatchesTheChoice(t *testing.T) {
	m, sess := discardPickerModel(t, reasoning.Medium)
	if !m.handleSlash("/model glm-5.2-air") {
		t.Fatal("/model with an argument was not handled")
	}
	if got := sess.ReasoningEffort(); got != reasoning.Medium {
		t.Fatalf("effort in force = %q, want the new model's default medium", got)
	}
	if body := transcript(m); strings.Contains(body, "discarded") {
		t.Fatalf("a switch that changed nothing observable announced a loss:\n%s", body)
	}

	sess2, res, out, term := replSession(t, reasoning.Medium)
	if _, _, err := handleSlash("/model glm-5.2-air", sess2, res, false, term); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "discarded") {
		t.Fatalf("plain surface announced a loss for an unchanged level:\n%s", out.String())
	}
}
