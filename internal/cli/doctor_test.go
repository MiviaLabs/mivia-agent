package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// writePromptBudgetConfig writes a deepseek doctor config into dir and
// returns its path. profileWindow/profileOutput set the active model's
// context_window_tokens and max_output_tokens; maxPromptTokens, when
// non-empty, emits a "[chat] max_prompt_tokens = <value>" line.
func writePromptBudgetConfig(t *testing.T, dir string, profileWindow, profileOutput int, maxPromptTokens string) string {
	t.Helper()
	path := filepath.Join(dir, "mivia.toml")
	chat := ""
	if maxPromptTokens != "" {
		chat = fmt.Sprintf("\n[chat]\nmax_prompt_tokens = %s\n", maxPromptTokens)
	}
	body := fmt.Sprintf(`[provider]
name = "deepseek"
api_key_env = "DEEPSEEK_API_KEY"

[providers.deepseek]
base_url = "https://api.deepseek.com/v1"
models = [
  { name = "deepseek-v4-flash", context_window_tokens = %d, max_output_tokens = %d },
]
default_model = "deepseek-v4-flash"
%s`, profileWindow, profileOutput, chat)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runDoctorForPromptBudget writes a config into a fresh temp dir, runs doctor
// against it, and returns the captured stdout/stderr. The returned error is
// deliberately ignored: the prompt-budget advisory contract is about the
// printed output, not the doctor exit status.
func runDoctorForPromptBudget(t *testing.T, profileWindow, profileOutput int, maxPromptTokens string) (stdout, stderr string) {
	t.Helper()
	configPath := writePromptBudgetConfig(t, t.TempDir(), profileWindow, profileOutput, maxPromptTokens)
	var out, errOut strings.Builder
	_ = runDoctorWithIO([]string{"--config", configPath}, &out, &errOut)
	return out.String(), errOut.String()
}

// TestDoctorPromptBudgetAdvisoryShown: with an unbounded prompt budget
// (max_prompt_tokens unset, context window 1000000 minus 384000 output
// reserve = 616000), doctor must print the prompt_budget advisory.
func TestDoctorPromptBudgetAdvisoryShown(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 1000000, 384000, "")
	if !strings.Contains(stdout, "prompt_budget: unbounded (616000 tokens)") {
		t.Fatalf("stdout missing unbounded prompt_budget advisory:\n%s", stdout)
	}
	if !strings.Contains(stdout, "recommended 200000") {
		t.Fatalf("stdout missing 'recommended 200000' hint:\n%s", stdout)
	}
}

// TestDoctorPromptBudgetAdvisoryAbsentWhenCapped: an explicit
// [chat] max_prompt_tokens = 200000 must suppress the advisory entirely.
func TestDoctorPromptBudgetAdvisoryAbsentWhenCapped(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 1000000, 384000, "200000")
	if strings.Contains(stdout, "prompt_budget") {
		t.Fatalf("stdout contains prompt_budget advisory despite [chat] max_prompt_tokens:\n%s", stdout)
	}
}

// TestDoctorPromptBudgetAdvisoryAbsentForSmallWindow: an active prompt budget
// (200000 - 128000 = 72000) at or below 200000 must not print the advisory.
func TestDoctorPromptBudgetAdvisoryAbsentForSmallWindow(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 200000, 128000, "")
	if strings.Contains(stdout, "prompt_budget") {
		t.Fatalf("stdout contains prompt_budget advisory for a small context window:\n%s", stdout)
	}
}

// TestDoctorPromptBudgetAdvisoryAbsentAtExactCap: a budget of exactly 200000
// (300000 window minus 100000 reserve) must not print the advisory - the guard
// is '<= cap', not '< cap'.
func TestDoctorPromptBudgetAdvisoryAbsentAtExactCap(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 300000, 100000, "")
	if strings.Contains(stdout, "prompt_budget") {
		t.Fatalf("stdout contains prompt_budget advisory at the exact cap:\n%s", stdout)
	}
}

// TestDoctorPromptBudgetAdvisoryAbsentWhenCapAboveBudget: an explicit cap above
// the window-derived budget (500000 vs 616000) must still suppress the advisory
// - the guard is on MaxPromptTokens being set, not on the cap binding.
func TestDoctorPromptBudgetAdvisoryAbsentWhenCapAboveBudget(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 1000000, 384000, "500000")
	if strings.Contains(stdout, "prompt_budget") {
		t.Fatalf("stdout contains prompt_budget advisory with an explicit cap above the budget:\n%s", stdout)
	}
}

// TestDoctorHumanOllamaLoopbackKeyless: a loopback Ollama endpoint
// (http://127.0.0.1:11434/v1) is a local server that performs no API-key
// auth, so the human doctor path must report ok even when OLLAMA_API_KEY is
// unset - no MISSING api_key line on stdout, no "not ready for chat" notice
// on stderr. This pins the keyless ollama-loopback relaxation end to end.
func TestDoctorHumanOllamaLoopbackKeyless(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[provider]
name = "ollama"

[providers.ollama]
base_url = "http://127.0.0.1:11434/v1"
models = [{ name = "qwen3:8b", context_window_tokens = 32768 }]
default_model = "qwen3:8b"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Defensively ensure the keyless case is exercised: no OLLAMA_API_KEY
	// may leak in from the host environment.
	t.Setenv("OLLAMA_API_KEY", "")

	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		AllowMissingConfig: true,
	})
	if err != nil {
		t.Fatalf("config.Load failed for ollama loopback config: %v", err)
	}
	if res.ProviderName != "ollama" || res.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("resolved provider/base_url = %q/%q", res.ProviderName, res.BaseURL)
	}

	var stdout, stderr strings.Builder
	if err := runDoctorWithIO([]string{"--config", cfgPath}, &stdout, &stderr); err != nil {
		t.Fatalf("doctor error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "status:     ok") {
		t.Fatalf("stdout missing 'status:     ok':\n%s", out)
	}
	if strings.Contains(out, "set OLLAMA_API_KEY") {
		t.Fatalf("stdout contains 'set OLLAMA_API_KEY' for keyless ollama loopback:\n%s", out)
	}
	if strings.Contains(stderr.String(), "not ready for chat") {
		t.Fatalf("stderr contains 'not ready for chat':\n%s", stderr.String())
	}
	// The keyless ollama screen must state the honest reason the key is not
	// required: a local daemon (loopback) performs no API-key auth.
	if !strings.Contains(out, "not required (local daemon)") {
		t.Fatalf("stdout missing 'not required (local daemon)' for keyless ollama loopback:\n%s", out)
	}
	// The keyless ollama screen must NOT claim the key is set: the value is
	// absent, so an "api_key:    set" line would be a lie.
	if strings.Contains(out, "api_key:    set") {
		t.Fatalf("stdout contains 'api_key:    set' for keyless ollama loopback:\n%s", out)
	}
}
