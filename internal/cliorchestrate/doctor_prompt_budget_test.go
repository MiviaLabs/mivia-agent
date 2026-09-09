package cliorchestrate

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
	_ = RunDoctorWithIO([]string{"--config", configPath}, &out, &errOut)
	return out.String(), errOut.String()
}

// TestDoctorReportsTheWindowDerivedBudget: with [chat] max_prompt_tokens
// unset the budget is the model window minus the output reserve, which is a
// bound. Doctor must report that number as a fact and must not call it
// unbounded or push a fixed cap, which on a large-window model amounted to
// advising the operator to discard most of the capacity they pay for.
func TestDoctorReportsTheWindowDerivedBudget(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 1000000, 384000, "")
	if !strings.Contains(stdout, "prompt_budget: 967232 tokens (from the model window)") {
		t.Fatalf("stdout missing the window-derived prompt_budget line:\n%s", stdout)
	}
	for _, banned := range []string{"unbounded", "recommended"} {
		if strings.Contains(stdout, banned) {
			t.Errorf("stdout still says %q about an uncapped budget:\n%s", banned, stdout)
		}
	}
}

// TestDoctorNamesTheWindowACapDiscards is the diagnosis worth making: a cap
// holding the budget far below the model's window is invisible everywhere
// else and makes a large model read as a small one. The threshold matches
// ports.ModelInfo.BudgetIsCapped so the doctor and the sidebar agree.
func TestDoctorNamesTheWindowACapDiscards(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 1000000, 384000, "200000")
	want := "prompt_budget: 200000 tokens (capped by [chat] max_prompt_tokens; the model window is 1000000)"
	if !strings.Contains(stdout, want) {
		t.Fatalf("stdout does not name the window the cap discards:\n%s", stdout)
	}
}

// TestDoctorReportsAModestCapWithoutTheWindow: a cap that leaves most of the
// window in play is a normal setting, not a finding, so it is reported
// without the window comparison that flags a discarding cap.
func TestDoctorReportsAModestCapWithoutTheWindow(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 1000000, 384000, "600000")
	if !strings.Contains(stdout, "prompt_budget: 600000 tokens (capped by [chat] max_prompt_tokens)") {
		t.Fatalf("stdout missing the plain capped line:\n%s", stdout)
	}
	if strings.Contains(stdout, "the model window is") {
		t.Errorf("a modest cap was reported as if it discarded the window:\n%s", stdout)
	}
}

// TestDoctorReportsSmallWindowsToo: the budget line is the only place doctor
// states the number the gauge and compaction actually use, so it is reported
// for every model, not only large ones.
func TestDoctorReportsSmallWindowsToo(t *testing.T) {
	stdout, _ := runDoctorForPromptBudget(t, 200000, 128000, "")
	if !strings.Contains(stdout, "prompt_budget: 167232 tokens (from the model window)") {
		t.Fatalf("stdout missing the prompt_budget line for a small window:\n%s", stdout)
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
	if err := RunDoctorWithIO([]string{"--config", cfgPath}, &stdout, &stderr); err != nil {
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
