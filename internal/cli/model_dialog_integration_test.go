package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type providerModelCapture struct {
	provider string
	model    string
	requests []provider.Request
}

func (c *providerModelCapture) Name() string { return c.provider }

func (c *providerModelCapture) Chat(_ context.Context, req provider.Request) (string, error) {
	return c.ChatStream(context.Background(), req, io.Discard)
}

func (c *providerModelCapture) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	req.Messages = append([]provider.Message(nil), req.Messages...)
	c.requests = append(c.requests, req)
	reply := c.provider + "/" + c.model
	_, _ = io.WriteString(w, reply)
	return reply, nil
}

func (c *providerModelCapture) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	req.Messages = append([]provider.Message(nil), req.Messages...)
	c.requests = append(c.requests, req)
	return &provider.Response{Content: c.provider + "/" + c.model, FinishReason: "stop"}, nil
}

func loadPickerConfig(t *testing.T) *config.Resolved {
	return loadPickerConfigWithEnv(t, "DEEPSEEK_API_KEY=picker-key\n")
}

func loadPickerConfigWithEnv(t *testing.T, envContents string) *config.Resolved {
	t.Helper()
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte(envContents), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	body := "env_file = \"" + filepath.ToSlash(env) + "\"\n\n" + `[provider]
name = "deepseek"

[providers.deepseek]
models = [
  { name = "deepseek/one", context_window_tokens = 128000 },
  { name = "deepseek/two", context_window_tokens = 128000 },
]

[providers.openrouter]
models = [
  { name = "openai/gpt-4o-mini", context_window_tokens = 128000 },
]

[chat]
max_tokens = 8192
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestIntegrationModelDialogShowsCatalogAndCommitsSelection(t *testing.T) {
	res := loadPickerConfig(t)
	m := newTUIModel(chat.NewSession(res, welcomeStubCompleter{}), res, true)
	m.mode = modeChat
	m.width, m.height, m.ready = 90, 24, true
	m.handleSlash("/model")
	if m.modelDlg == nil {
		t.Fatal("bare /model did not open picker")
	}
	view := m.View()
	for _, want := range []string{"deepseek", "deepseek/one", "deepseek/two", "openrouter", "openai/gpt-4o-mini", "credential unavailable"} {
		if !strings.Contains(stripANSI(view), want) {
			t.Fatalf("picker missing %q:\n%s", want, stripANSI(view))
		}
	}
	// The selected marker follows the full provider/model identity, not the
	// model suffix. Move to the second active model and commit it.
	m.handleModelDialogKey("down")
	m.handleModelDialogKey("enter")
	if m.modelDlg != nil || m.session.CurrentSelection().Model != "deepseek/two" {
		t.Fatalf("selection=%+v dialog=%v", m.session.CurrentSelection(), m.modelDlg != nil)
	}
	if m.modelName != "deepseek/two" {
		t.Fatalf("header model=%q", m.modelName)
	}
}

func TestIntegrationModelDialogDisabledRowCannotCommit(t *testing.T) {
	res := loadPickerConfig(t)
	m := newTUIModel(chat.NewSession(res, welcomeStubCompleter{}), res, true)
	m.mode = modeChat
	m.width, m.height, m.ready = 90, 24, true
	m.openModelDialog()
	for i, row := range m.modelDlg.rows {
		if row.model == "openai/gpt-4o-mini" {
			m.modelDlg.cursor = i
			break
		}
	}
	m.handleModelDialogKey("enter")
	if m.modelDlg == nil {
		t.Fatal("disabled row closed picker")
	}
	if got := m.session.CurrentSelection(); got.ProviderName != "deepseek" || got.Model != "deepseek/one" {
		t.Fatalf("disabled row mutated selection: %+v", got)
	}
	if !strings.Contains(m.modelDlg.notice, "credential unavailable") {
		t.Fatalf("notice=%q", m.modelDlg.notice)
	}
}

func TestIntegrationModelDialogCommitsEnabledCrossProviderSelection(t *testing.T) {
	res := loadPickerConfigWithEnv(t, "DEEPSEEK_API_KEY=picker-key\nOPENROUTER_API_KEY=router-key\n")
	sess := chat.NewSession(res, welcomeStubCompleter{})
	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.width, m.height, m.ready = 90, 24, true
	m.openModelDialog()
	rowIndex := -1
	for i, row := range m.modelDlg.rows {
		if row.provider == "openrouter" && row.model == "openai/gpt-4o-mini" {
			rowIndex = i
			break
		}
	}
	if rowIndex < 0 {
		t.Fatal("cross-provider row missing")
	}
	layout := m.modelDlg.layout(m.width, m.height)
	if !m.handleModalMouse(tea.MouseMsg{Y: layout.rect.y + 1 + rowIndex - m.modelDlg.scroll, Type: tea.MouseLeft}) {
		t.Fatal("model dialog did not consume row click")
	}
	if m.modelDlg.cursor != rowIndex {
		t.Fatalf("clicked cursor = %d, want %d", m.modelDlg.cursor, rowIndex)
	}
	m.handleModelDialogKey("enter")
	if m.modelDlg != nil {
		t.Fatal("enabled cross-provider selection left picker open")
	}
	if got := sess.CurrentSelection(); got.ProviderName != "openrouter" || got.Model != "openai/gpt-4o-mini" {
		t.Fatalf("selection = %+v", got)
	}
}

func TestIntegrationModelDialogDirectCommandUsesCurrentProvider(t *testing.T) {
	res := loadPickerConfigWithEnv(t, "DEEPSEEK_API_KEY=picker-key\nOPENROUTER_API_KEY=router-key\n")
	sess := chat.NewSession(res, welcomeStubCompleter{})
	if err := switchModelCommand(sess, res, "openrouter", "openai/gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	termOutput := new(strings.Builder)
	term := &Terminal{out: termOutput}
	if _, _, err := handleSlash("/model", sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(termOutput.String(), "available: openai/gpt-4o-mini") {
		t.Fatalf("current-provider choices = %q", termOutput.String())
	}
}

// pickerEffortConfig gives the active model and one alternative distinct
// defaults, so a row showing the wrong one is unambiguous.
func pickerEffortConfig() *config.Resolved {
	return &config.Resolved{
		ProviderName: "zai",
		Model:        "glm-5.2",
		Models:       []string{"glm-5.2", "glm-5.2-air", "glm-4.6"},
		ModelProfiles: []config.ModelSpec{
			{
				Name: "glm-5.2", ContextWindowTokens: 200000,
				ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
				Reasoning:        reasoning.High,
				ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{
				Name: "glm-5.2-air", ContextWindowTokens: 200000,
				ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium},
				Reasoning:        reasoning.Medium,
				ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: "glm-4.6", ContextWindowTokens: 200000},
		},
	}
}

func pickerLineFor(t *testing.T, view, model string) string {
	t.Helper()
	for _, line := range strings.Split(stripANSI(view), "\n") {
		if strings.Contains(line, model) {
			return line
		}
	}
	t.Fatalf("model %q missing from picker:\n%s", model, stripANSI(view))
	return ""
}

// The current row carries the ● marker, so its annotation is read as a
// statement about the running session, not about a model on offer.
func TestIntegrationModelDialogCurrentRowShowsTheEffectiveEffort(t *testing.T) {
	res := pickerEffortConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	if err := sess.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.openModelDialog()
	view, _ := m.modelDlg.ViewAt(90, 24)

	current := pickerLineFor(t, view, "glm-5.2 ")
	if !strings.Contains(current, "effort: low") {
		t.Fatalf("current row does not show the effort in force: %q", current)
	}
	// Every other row still describes what selecting it would give the user.
	if other := pickerLineFor(t, view, "glm-5.2-air"); !strings.Contains(other, "effort: medium") {
		t.Fatalf("non-current row lost its configured default: %q", other)
	}
	if plain := pickerLineFor(t, view, "glm-4.6"); strings.Contains(plain, "effort") {
		t.Fatalf("a model offering nothing was annotated: %q", plain)
	}
}

func TestIntegrationModelDialogStaysWithinTinyCanvases(t *testing.T) {
	res := loadPickerConfig(t)
	d := newModelDialog(res.ModelCatalog(), chat.Selection{ProviderName: res.ProviderName, Model: res.Model}, "", false)
	for _, size := range []struct{ width, height int }{{1, 1}, {2, 8}, {24, 2}, {90, 24}} {
		view, layout := d.ViewAt(size.width, size.height)
		if layout.rect.x < 0 || layout.rect.y < 0 || layout.rect.x+layout.rect.w > size.width || layout.rect.y+layout.rect.h > size.height {
			t.Fatalf("size %dx%d out-of-bounds layout: %+v", size.width, size.height, layout)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > layout.rect.w {
				t.Fatalf("size %dx%d line width=%d rect=%d: %q", size.width, size.height, ansi.StringWidth(line), layout.rect.w, stripANSI(line))
			}
		}
	}
}

func TestIntegrationModelDialogUsesSessionBindingFactory(t *testing.T) {
	res := loadPickerConfig(t)
	sess := chat.NewSession(res, welcomeStubCompleter{})
	called := false
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		called = true
		return chat.ModelBinding{
			ProviderName: providerName,
			Model:        model,
			Completer:    welcomeStubCompleter{},
			Profile:      config.ModelSpec{Name: model, ContextWindowTokens: 128000},
		}, nil
	})
	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.handleSlash("/model deepseek/two")
	if !called {
		t.Fatal("model switch bypassed the session binding factory")
	}
	if got := sess.CurrentSelection(); got.ProviderName != "deepseek" || got.Model != "deepseek/two" {
		t.Fatalf("selection = %+v", got)
	}
}

func TestIntegrationProviderModelSwitchRoutesActiveSessionTurns(t *testing.T) {
	res := loadPickerConfigWithEnv(t, "DEEPSEEK_API_KEY=picker-key\nOPENROUTER_API_KEY=router-key\n")
	captures := map[string]*providerModelCapture{}
	for _, key := range []string{"deepseek/deepseek/one", "openrouter/openai/gpt-4o-mini"} {
		parts := strings.SplitN(key, "/", 2)
		captured := &providerModelCapture{provider: parts[0], model: parts[1]}
		captures[key] = captured
	}
	initial := captures["deepseek/deepseek/one"]
	sess := chat.NewSession(res, initial)
	sess.UseTools = false
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		captured, ok := captures[providerName+"/"+model]
		if !ok {
			return chat.ModelBinding{}, fmt.Errorf("missing test binding %s/%s", providerName, model)
		}
		return chat.ModelBinding{
			ProviderName: providerName,
			Model:        model,
			Completer:    captured,
			Profile:      config.ModelSpec{Name: model, ContextWindowTokens: 128000},
		}, nil
	})

	m := newTUIModel(sess, res, false)
	m.mode = modeChat
	m.ready = true
	m.width, m.height = 100, 30

	if _, err := sess.SendUser(context.Background(), "first turn", io.Discard); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if got := sess.CurrentSelection(); got.ProviderName != "deepseek" || got.Model != "deepseek/one" {
		t.Fatalf("initial selection = %+v", got)
	}

	m.openModelDialog()
	rowIndex := -1
	for i, row := range m.modelDlg.rows {
		if row.provider == "openrouter" && row.model == "openai/gpt-4o-mini" {
			rowIndex = i
			break
		}
	}
	if rowIndex < 0 {
		t.Fatal("enabled cross-provider model row missing")
	}
	m.modelDlg.cursor = rowIndex
	m.handleModelDialogKey("enter")
	if got := sess.CurrentSelection(); got.ProviderName != "openrouter" || got.Model != "openai/gpt-4o-mini" {
		t.Fatalf("switched selection = %+v", got)
	}

	if _, err := sess.SendUser(context.Background(), "second turn", io.Discard); err != nil {
		t.Fatalf("second turn after switch: %v", err)
	}
	if len(initial.requests) != 1 || initial.requests[0].Model != "deepseek/one" {
		t.Fatalf("initial provider requests = %+v", initial.requests)
	}
	switched := captures["openrouter/openai/gpt-4o-mini"]
	if len(switched.requests) != 1 || switched.requests[0].Model != "openai/gpt-4o-mini" {
		t.Fatalf("switched provider requests = %+v", switched.requests)
	}
	if got := sess.MessagesCopy(); len(got) < 4 || got[len(got)-2].Content != "second turn" {
		t.Fatalf("active session history after provider/model switch = %+v", got)
	}
}

func TestIntegrationModelDialogRejectsSwitchDuringActiveTurn(t *testing.T) {
	res := loadPickerConfigWithEnv(t, "DEEPSEEK_API_KEY=picker-key\nOPENROUTER_API_KEY=router-key\n")
	sess := chat.NewSession(res, welcomeStubCompleter{})
	m := newTUIModel(sess, res, false)
	m.mode = modeChat
	m.ready = true
	m.waiting = true
	m.width, m.height = 100, 30
	m.openModelDialog()
	for i, row := range m.modelDlg.rows {
		if row.provider == "openrouter" && row.model == "openai/gpt-4o-mini" {
			m.modelDlg.cursor = i
			break
		}
	}

	m.handleModelDialogKey("enter")

	if m.modelDlg == nil {
		t.Fatal("model dialog closed during an active turn")
	}
	if !strings.Contains(m.modelDlg.notice, "finish current work") {
		t.Fatalf("active-turn rejection notice = %q", m.modelDlg.notice)
	}
	if got := sess.CurrentSelection(); got.ProviderName != "deepseek" || got.Model != "deepseek/one" {
		t.Fatalf("active-turn switch mutated selection: %+v", got)
	}
}
