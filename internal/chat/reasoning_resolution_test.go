package chat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// loadReasoningConfig builds the binding from TOML rather than from a literal.
// A hand-built ModelSpec can only hold a dialect somebody wrote down, so it
// cannot express the commonest entry of all - one that names levels and leaves
// the wire shape to the provider - which is the shape these tests exist for.
func loadReasoningConfig(t *testing.T, defaultModel, modelLines string) *config.Resolved {
	t.Helper()
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("ZAI_API_KEY=resolution-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	body := "env_file = \"" + filepath.ToSlash(env) + "\"\n\n" + `[provider]
name = "zai"

[providers.zai]
models = [
` + modelLines + `
]
default_model = "` + defaultModel + `"

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

// The session is what every read-only surface asks for the dial, so it must
// hand back the dialect the request will actually carry. Handing back the empty
// string leaves each caller to resolve it, which is how the display layer came
// to report a model with no dialect as a model with no reasoning.
func TestIntegrationReasoningSettingResolvesTheProviderDialect(t *testing.T) {
	// The thinking dialect cannot carry depth, so off plus one graded level is
	// the widest set config.Load accepts without an explicit dialect here.
	res := loadReasoningConfig(t, "glm-4.7",
		`  { name = "glm-4.7", context_window_tokens = 200000, reasoning_efforts = ["off", "high"], reasoning = "high" },`)
	s := NewSession(res, &requestCaptureCompleter{})
	want := reasoning.Setting{Level: reasoning.High, Dialect: reasoning.DialectThinking}
	if got := s.ReasoningSetting(); got != want {
		t.Fatalf("ReasoningSetting = %+v, want %+v", got, want)
	}
	if err := s.SetReasoningEffort(reasoning.Off); err != nil {
		t.Fatalf("the session refused a level the model declares: %v", err)
	}
	want.Level = reasoning.Off
	if got := s.ReasoningSetting(); got != want {
		t.Fatalf("after /effort off ReasoningSetting = %+v, want %+v", got, want)
	}
}

// A configured dialect must survive resolution untouched, or naming one would
// silently get the provider's default instead.
func TestIntegrationReasoningSettingKeepsAConfiguredDialect(t *testing.T) {
	res := loadReasoningConfig(t, "glm-5.2",
		`  { name = "glm-5.2", context_window_tokens = 200000, reasoning_efforts = ["low", "medium", "high"], reasoning = "medium", reasoning_dialect = "thinking_effort" },`)
	s := NewSession(res, &requestCaptureCompleter{})
	want := reasoning.Setting{Level: reasoning.Medium, Dialect: reasoning.DialectThinkingEffort}
	if got := s.ReasoningSetting(); got != want {
		t.Fatalf("ReasoningSetting = %+v, want %+v", got, want)
	}
}

// A dialect with no declared levels is a capability statement about a model
// that is dialled off. The resolved setting must not read as an offer, which is
// a question only the declared set can answer.
func TestIntegrationADialectWithoutEffortsOffersNothing(t *testing.T) {
	res := loadReasoningConfig(t, "glm-4.8",
		`  { name = "glm-4.8", context_window_tokens = 200000, reasoning_dialect = "openai" },`)
	s := NewSession(res, &requestCaptureCompleter{})
	if got := s.ReasoningChoices(); len(got) != 0 {
		t.Fatalf("ReasoningChoices = %v for a model that declares none", got)
	}
	want := reasoning.Setting{Dialect: reasoning.DialectOpenAI}
	if got := s.ReasoningSetting(); got != want {
		t.Fatalf("ReasoningSetting = %+v, want %+v", got, want)
	}
}
