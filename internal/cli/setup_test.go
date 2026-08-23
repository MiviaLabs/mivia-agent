package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
)

// runSetupCapture runs setup with controlled IO and returns the summary text.
func runSetupCapture(t *testing.T, args []string, stdin string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := runSetupWithIO(args, &buf, strings.NewReader(stdin))
	return buf.String(), err
}

func TestSetupWritesKeyToNewEnvFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", "openrouter",
		"--key", "sk-test-0000",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("setup error = %v", err)
	}
	if !strings.Contains(out, "provider:   openrouter") {
		t.Fatalf("setup summary lacks the provider: %q", out)
	}
	entries, err := sdkenvfile.Load(envPath)
	if err != nil {
		t.Fatalf("load written env file: %v", err)
	}
	if entries["OPENROUTER_API_KEY"] != "sk-test-0000" {
		t.Fatalf("env key = %q, want sk-test-0000", entries["OPENROUTER_API_KEY"])
	}
	st, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits; a mode of 0600 is reported as
	// 0666 there, so the exact-bit contract is asserted only on Unix.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("env file mode = %o, want 600", st.Mode().Perm())
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("default config was not written: %v", err)
	}
	if raw, _ := os.ReadFile(cfgPath); !strings.Contains(string(raw), "openai/gpt-5.6-luna") {
		t.Fatalf("default config lacks the shipped model: %q", raw)
	}
}

func TestSetupPreservesExistingEnvKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("OTHER_KEY=keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "mivia.toml")
	if _, err := runSetupCapture(t, []string{
		"--key", "sk-test-0001",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, ""); err != nil {
		t.Fatalf("setup error = %v", err)
	}
	entries, err := sdkenvfile.Load(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if entries["OTHER_KEY"] != "keep-me" {
		t.Fatalf("existing key lost: %#v", entries)
	}
	if entries["OPENROUTER_API_KEY"] != "sk-test-0001" {
		t.Fatalf("new key missing: %#v", entries)
	}
}

func TestSetupReadsKeyFromEnvVar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "sk-test-0002")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	if _, err := runSetupCapture(t, []string{
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, ""); err != nil {
		t.Fatalf("setup error = %v", err)
	}
	entries, err := sdkenvfile.Load(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if entries["OPENROUTER_API_KEY"] != "sk-test-0002" {
		t.Fatalf("env-var key not written: %#v", entries)
	}
}

func TestSetupRequiresKeyWhenNonInteractive(t *testing.T) {
	dir := t.TempDir()
	_, err := runSetupCapture(t, []string{
		"--env-file", filepath.Join(dir, ".env"),
		"--config", filepath.Join(dir, "mivia.toml"),
		"--yes",
	}, "")
	if err == nil {
		t.Fatal("setup with no key returned nil error")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("setup error = %v, want missing-key message", err)
	}
}

func TestSetupSkipsConfigForOtherProvider(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", "zai",
		"--key", "sk-test-0003",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("setup error = %v", err)
	}
	if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
		t.Fatalf("config written for a non-default provider (err=%v)", statErr)
	}
	if !strings.Contains(out, "[providers.zai]") {
		t.Fatalf("summary lacks the provider guidance: %q", out)
	}
}

func TestSetupOllamaKeylessNeedsNoKey(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", "ollama",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("keyless ollama setup error = %v", err)
	}
	if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
		t.Fatalf("env file created for keyless ollama (stat err=%v)", statErr)
	}
	for _, want := range []string{"local daemon", "Cloud", "http://127.0.0.1:11434/v1", "mivia doctor"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(written)") {
		t.Fatalf("summary claims a written key file for keyless ollama:\n%s", out)
	}
}

// A whitespace-only OLLAMA_API_KEY must count as missing so setup takes the
// keyless-ollama branch instead of writing a junk key line to the env file.
func TestSetupOllamaWhitespaceKeyCountsAsKeyless(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "   ")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", "ollama",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("keyless ollama setup error = %v", err)
	}
	if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
		t.Fatalf("env file created for whitespace-key ollama (stat err=%v)", statErr)
	}
	for _, want := range []string{"local daemon", "Cloud", "http://127.0.0.1:11434/v1", "mivia doctor"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(written)") {
		t.Fatalf("summary claims a written key file for keyless ollama:\n%s", out)
	}
}

// A case-variant provider name must still enter the keyless-ollama branch and
// resolve the upper-cased OLLAMA_API_KEY env name.
func TestSetupOllamaCaseVariantIsKeyless(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", "Ollama",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("keyless Ollama setup error = %v", err)
	}
	if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
		t.Fatalf("env file created for keyless Ollama (stat err=%v)", statErr)
	}
	if !strings.Contains(out, "provider:   ollama") {
		t.Fatalf("summary did not normalize the provider name:\n%s", out)
	}
	for _, want := range []string{"local daemon", "http://127.0.0.1:11434/v1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestSetupOllamaWithKeyWritesEnvFile(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", "ollama",
		"--key", "sk-test",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("setup error = %v", err)
	}
	entries, err := sdkenvfile.Load(envPath)
	if err != nil {
		t.Fatalf("load written env file: %v", err)
	}
	if entries["OLLAMA_API_KEY"] != "sk-test" {
		t.Fatalf("env key = %q, want sk-test", entries["OLLAMA_API_KEY"])
	}
	if !strings.Contains(out, "(written)") {
		t.Fatalf("summary lacks the (written) marker for keyed ollama:\n%s", out)
	}
	if strings.Contains(out, "sk-test") {
		t.Fatalf("summary leaks the key value:\n%s", out)
	}
}

func TestSetupDeepseekNoKeyStillErrors(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	dir := t.TempDir()
	_, err := runSetupCapture(t, []string{
		"--provider", "deepseek",
		"--env-file", filepath.Join(dir, ".env"),
		"--config", filepath.Join(dir, "mivia.toml"),
		"--yes",
	}, "")
	if err == nil {
		t.Fatal("deepseek setup with no key returned nil error")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("setup error = %v, want missing-key message", err)
	}
}

func TestSetupKeepsExistingConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	existing := "[provider]\nname = \"deepseek\"\n"
	if err := os.WriteFile(cfgPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runSetupCapture(t, []string{
		"--key", "sk-test-0004",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, ""); err != nil {
		t.Fatalf("setup error = %v", err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != existing {
		t.Fatalf("existing config changed: %q", raw)
	}
}

func TestSetupArgErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--bogus"}, "unknown flag"},
		{"provider missing value", []string{"--provider"}, "--provider requires"},
		{"key missing value", []string{"--key"}, "--key requires"},
		{"unexpected positional", []string{"extra"}, "unexpected argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSetupCapture(t, append(tc.args,
				"--env-file", filepath.Join(dir, ".env"),
				"--config", filepath.Join(dir, "mivia.toml"),
			), "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestExecuteSetupWritesFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	done := captureStdout(t)
	defer done()
	err := Execute([]string{"setup",
		"--key", "sk-test-0005",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	})
	_ = done()
	if err != nil {
		t.Fatalf("Execute([setup ...]) error = %v", err)
	}
	if _, statErr := os.Stat(envPath); statErr != nil {
		t.Fatalf("env file missing after Execute: %v", statErr)
	}
	if _, statErr := os.Stat(cfgPath); statErr != nil {
		t.Fatalf("config missing after Execute: %v", statErr)
	}
}

// A whitespace-only DEEPSEEK_API_KEY must count as missing: setup must not
// accept padded whitespace as a key and must fail with the no-key error.
func TestSetupDeepseekWhitespaceKeyStillErrors(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "   ")
	dir := t.TempDir()
	_, err := runSetupCapture(t, []string{
		"--provider", "deepseek",
		"--env-file", filepath.Join(dir, ".env"),
		"--config", filepath.Join(dir, "mivia.toml"),
		"--yes",
	}, "")
	if err == nil {
		t.Fatal("deepseek setup with a whitespace key returned nil error")
	}
	if !strings.Contains(err.Error(), "setup: no API key") {
		t.Fatalf("setup error = %v, want missing-key message", err)
	}
}

// A padded --key value must be trimmed before it reaches the env file: the
// file must contain exactly OPENROUTER_API_KEY=sk-test with no whitespace.
func TestSetupTrimsPaddedKeyValue(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	if _, err := runSetupCapture(t, []string{
		"--provider", "openrouter",
		"--key", " sk-test ",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, ""); err != nil {
		t.Fatalf("setup error = %v", err)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read written env file: %v", err)
	}
	if string(raw) != "OPENROUTER_API_KEY=sk-test\n" {
		t.Fatalf("env file = %q, want exactly %q", raw, "OPENROUTER_API_KEY=sk-test\n")
	}
	entries, err := sdkenvfile.Load(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if entries["OPENROUTER_API_KEY"] != "sk-test" {
		t.Fatalf("env key = %q, want sk-test", entries["OPENROUTER_API_KEY"])
	}
}

// A padded provider name must be trimmed before the keyless-ollama decision:
// setup must take the keyless branch, exit 0, and write no env file.
func TestSetupOllamaPaddedProviderIsKeyless(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", " ollama ",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("keyless ollama setup error = %v", err)
	}
	if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
		t.Fatalf("env file created for padded-provider ollama (stat err=%v)", statErr)
	}
	if !strings.Contains(out, "provider:   ollama") {
		t.Fatalf("summary did not trim the provider name:\n%s", out)
	}
}

// An empty --provider= value must be rejected up front by the argument parser.
func TestSetupEmptyProviderFlagErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runSetupCapture(t, []string{
		"--provider=",
		"--env-file", filepath.Join(dir, ".env"),
		"--config", filepath.Join(dir, "mivia.toml"),
		"--yes",
	}, "")
	if err == nil {
		t.Fatal("setup with an empty --provider returned nil error")
	}
	if !strings.Contains(err.Error(), "setup: --provider requires a name") {
		t.Fatalf("setup error = %v, want --provider requires a name", err)
	}
}
