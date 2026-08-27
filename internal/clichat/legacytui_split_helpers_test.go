package clichat

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// The helpers in this file are package-local copies of internal/legacytui
// test helpers of the same name: they are shared by several cli-only tests
// but their original home moved to internal/legacytui with the TUI tests
// that also need them. Go test files are not part of a package's
// importable surface, so a helper shared by tests in both packages must
// exist in each.

// agentFixture, intPtr, and fixtureAgentStateWithTools: agent_switch_test.go.
type agentFixture struct {
	prompt   string
	tools    []string
	maxTurns *int
}

func intPtr(n int) *int { return &n }

func fixtureAgentStateWithTools(t *testing.T, fx map[string]agentFixture) *AgentSessionState {
	t.Helper()
	reg := agents.NewRegistry()
	for name, f := range fx {
		a := agents.ResolvedAgent{
			Name:           name,
			Description:    name + " desc",
			SystemPrompt:   f.prompt,
			EffectiveTools: append([]string(nil), f.tools...),
			MaxTurns:       f.maxTurns,
		}
		if err := reg.Publish(a); err != nil {
			t.Fatal(err)
		}
	}
	return &AgentSessionState{
		Registry:           reg,
		AllowProjectSkills: true,
		WorkspaceRoot:      t.TempDir(),
		Global:             config.AgentsGlobal{FailOnEmptyToolset: true},
	}
}

// blockKinds, hasAssistantText, hasBlockKind, and kindOrderContains:
// tui_tools_test.go.
func hasAssistantText(blocks []ChatBlock, substr string) bool {
	for _, b := range blocks {
		if b.Kind == ChatBlockAssistant && strings.Contains(b.Text, substr) {
			return true
		}
	}
	return false
}

func hasBlockKind(blocks []ChatBlock, k ChatBlockKind) bool {
	for _, b := range blocks {
		if b.Kind == k {
			return true
		}
	}
	return false
}

func blockKinds(blocks []ChatBlock) []ChatBlockKind {
	out := make([]ChatBlockKind, len(blocks))
	for i, b := range blocks {
		out[i] = b.Kind
	}
	return out
}

func kindOrderContains(have []ChatBlockKind, want ...ChatBlockKind) bool {
	i := 0
	for _, h := range have {
		if i < len(want) && h == want[i] {
			i++
		}
	}
	return i == len(want)
}

// dumpPlain: chatblock_rail_integration_test.go.
func dumpPlain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripAnsiOut(l)
	}
	return out
}

// effortThinker, effortPlain, and effortCatalogConfig: effort_dialog_integration_test.go.
const (
	effortThinker = "glm-5.2"
	effortPlain   = "glm-4.6"
)

func effortCatalogConfig() *config.Resolved {
	return &config.Resolved{
		ProviderName: "zai",
		Model:        effortThinker,
		Models:       []string{effortThinker, effortPlain},
		ModelProfiles: []config.ModelSpec{
			{
				Name: effortThinker, ContextWindowTokens: 200000,
				ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
				Reasoning:        reasoning.High,
				ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: effortPlain, ContextWindowTokens: 200000},
		},
	}
}

// installTestRedactionPolicy: toolpanel_privacy_test.go.
//
// The policy is process-wide state (see internal/redact), so every test that
// installs one must stay sequential. Do not add t.Parallel() to a test that
// calls this, and do not call it from a parallel test.
func installTestRedactionPolicy(t *testing.T) {
	t.Helper()
	policy, err := redact.Compile([]string{
		// Bearer first: the generic key/value rule below would otherwise
		// consume "Authorization: Bearer" and leave the credential behind.
		`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`,
		`(?i)(?:["']?)(?:api[_-]?key|authorization|password|secret|token|private[_-]?key)(?:["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^,\s}]+)`,
		`(?is)-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]+PRIVATE KEY-----|$)`,
	}, []string{"password", "token", "secret", "api_key", "authorization"}, redact.DefaultPlaceholder)
	if err != nil {
		t.Fatalf("compile test redaction policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(nil) })
}

// itoa is a small integer to string converter (no fmt import needed).
// liveness_stress_test.go.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	neg := n < 0
	if neg {
		n = -n
	}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// loadPickerConfig and loadPickerConfigWithEnv: model_dialog_integration_test.go.
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

// welcomeStubCompleter answers immediately so startAI workers do not panic.
// tui_welcome_input_test.go.
type welcomeStubCompleter struct{}

func (welcomeStubCompleter) Name() string { return "welcome-stub" }
func (welcomeStubCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "ok", nil
}
func (welcomeStubCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (welcomeStubCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok"}, nil
}

// assertManagedWorktreeActive: worktree_picker_instance_test.go.
func assertManagedWorktreeActive(t *testing.T, repoRoot string, worktree *vcs.WorktreeInfo) {
	t.Helper()
	resolved, err := vcs.Resolve(context.Background(), repoRoot, worktree.Name)
	if err != nil || resolved == nil {
		t.Fatalf("replacement worktree = %+v, %v", resolved, err)
	}
	instance, err := cliworktree.ReadWorktreeMarker(worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenRepositoryContextStore(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := cliworktree.WorktreeRoutePrincipal(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, worktree.Path); err != nil {
		t.Fatalf("replacement is not active: %v", err)
	}
}
