package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenHumanOutputOK is the exact byte output of runDoctorWithIO with a
// standard config, DEEPSEEK_API_KEY set, no --json, and no workspace agents.
// Any change to the human path formatting must update this constant; the
// byte-for-byte comparison in TestDoctorHumanOutputUnchanged will fail
// otherwise, guaranteeing the "byte-identical" contract.
const goldenHumanOutputOK = `mivia doctor
  config:     {{CONFIG_PATH}}
  env_file:   (none found; using process env only)
  provider:   deepseek
  model:      deepseek-v4-pro
  catalog:    deepseek/deepseek-v4-pro:128000
  base_url:   https://api.deepseek.com/v1
  api_key_env:DEEPSEEK_API_KEY
agents:
  collection: not present
  name: (none)
  source: (none)
  state: no definitions
  tools: (none)
  model: (none)
  turns: (none)
  name: root fallback
  source: compiled
  state: fallback (not selectable)
  tools: session defaults
  model: session binding
  turns: session default
workspace agent files: always loaded
workspace prompts/project skills: enabled
  api_key:    set (value redacted)
  status:     ok
`

// goldenHumanOutputMissingAPIKey is the exact byte output when DEEPSEEK_API_KEY
// is unset. The error text and stderr are separate; this captures stdout only.
const goldenHumanOutputMissingAPIKey = `mivia doctor
  config:     {{CONFIG_PATH}}
  env_file:   (none found; using process env only)
  provider:   deepseek
  model:      deepseek-v4-pro
  catalog:    deepseek/deepseek-v4-pro:128000
  base_url:   https://api.deepseek.com/v1
  api_key_env:DEEPSEEK_API_KEY
agents:
  collection: not present
  name: (none)
  source: (none)
  state: no definitions
  tools: (none)
  model: (none)
  turns: (none)
  name: root fallback
  source: compiled
  state: fallback (not selectable)
  tools: session defaults
  model: session binding
  turns: session default
workspace agent files: always loaded
workspace prompts/project skills: enabled
  api_key:    MISSING - set DEEPSEEK_API_KEY in environment or env file
`

// --- Helpers for --json tests ---

func setupDoctorJSONTest(t *testing.T) (configPath, workspace string, cleanup func()) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "SUPERSECRET_d61f8b")
	root := t.TempDir()
	cp := writeDoctorConfig(t, root)
	ws := t.TempDir()
	return cp, ws, func() {}
}

// writeDoctorConfigWithEnvPath creates a config file that explicitly references
// the given env_file path (must be absolute to avoid cwd-relative resolution).
func writeDoctorConfigWithEnvPath(t *testing.T, dir, envPath string) string {
	t.Helper()
	path := filepath.Join(dir, "mivia.toml")
	// Windows paths contain backslashes; TOML treats them as escapes ("\U"
	// in "C:\Users" is not valid), so double them for a literal backslash.
	envLiteral := strings.ReplaceAll(envPath, `\`, `\\`)
	body := "env_file = \"" + envLiteral + "\"\n\n" +
		"[provider]\n" +
		"name = \"deepseek\"\n\n" +
		"[providers.deepseek]\n" +
		"models = [{ name = \"deepseek-v4-pro\", context_window_tokens = 128000 }]\n" +
		"default_model = \"deepseek-v4-pro\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- Test Cases ---

func TestDoctorHumanOutputUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("doctor unexpected error: %v", err)
	}
	// Byte-for-byte golden comparison. Substitute the config path placeholder.
	text := out.String()
	expected := strings.ReplaceAll(goldenHumanOutputOK, "{{CONFIG_PATH}}", configPath)
	if text != expected {
		t.Errorf("human output changed.\n--- got ---\n%s\n--- want ---\n%s\n--- diff ---", text, expected)
	}
	// Verify no JSON artifacts leaked into human output.
	if strings.Contains(text, `"api_key_set"`) {
		t.Fatalf("JSON api_key_set key leaked into human output")
	}
	if strings.Contains(text, `"agent_catalog"`) {
		t.Fatalf("JSON agent_catalog key leaked into human output")
	}
}

func TestDoctorHumanOutputUnchangedMissingAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "missing DEEPSEEK_API_KEY") {
		t.Fatalf("doctor error = %v, want missing DEEPSEEK_API_KEY", err)
	}
	// Byte-for-byte golden comparison for stdout (stderr is separate).
	text := out.String()
	expected := strings.ReplaceAll(goldenHumanOutputMissingAPIKey, "{{CONFIG_PATH}}", configPath)
	if text != expected {
		t.Errorf("human output (missing API key) changed.\n--- got ---\n%s\n--- want ---\n%s\n--- diff ---", text, expected)
	}
	if strings.Contains(text, `"api_key_set"`) {
		t.Fatalf("JSON leaked into human output")
	}
}

func TestDoctorJSONValidWithAllFields(t *testing.T) {
	configPath, workspace, _ := setupDoctorJSONTest(t)
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("doctor --json unexpected error: %v", err)
	}
	// Must be valid JSON.
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatalf("doctor --json output is not valid JSON: %v\nraw: %s", err, out.String())
	}
	// Assert all required keys are present by checking struct fields.
	requiredKeys := []string{
		"config", "env_file", "env_file_loaded", "provider", "model",
		"model_catalog", "base_url", "api_key_env", "api_key_set",
		"agent_catalog", "warnings", "status",
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out.String()), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range requiredKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing required key %q in JSON output", key)
		}
	}
	// Type assertions.
	if dj.Status != "ok" {
		t.Errorf("status = %q, want ok", dj.Status)
	}
	// env_file_loaded should be a boolean (JSON encoding handles this).
	// agent_catalog should be an array.
	if dj.AgentCatalog == nil {
		t.Error("agent_catalog is nil, want array")
	}
}

func TestDoctorJSONAPIKeyNeverAppears(t *testing.T) {
	sentinel := "SUPERSECRET_d61f8b"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", sentinel)
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("doctor --json unexpected error: %v", err)
	}
	raw := out.String()
	// The sentinel API key must never appear in JSON output.
	if strings.Contains(raw, sentinel) {
		t.Fatalf("API key sentinel %q appears in JSON output", sentinel)
	}
	// There must be no "api_key" field (only api_key_set and api_key_env).
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["api_key"]; ok {
		t.Fatal("JSON output contains raw api_key field (must only have api_key_set)")
	}
	// api_key_set must be present and boolean true.
	keySetRaw, ok := m["api_key_set"]
	if !ok {
		t.Fatal("api_key_set is missing from JSON")
	}
	var keySet bool
	if err := json.Unmarshal(keySetRaw, &keySet); err != nil {
		t.Fatalf("api_key_set is not a boolean: %v", err)
	}
	if !keySet {
		t.Error("api_key_set = false, want true when key is set")
	}
}

func TestDoctorJSONErrorPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "") // Missing API key.
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("doctor --json should return error when API key is missing")
	}
	if !strings.Contains(err.Error(), "missing DEEPSEEK_API_KEY") {
		t.Fatalf("doctor error = %v, want missing DEEPSEEK_API_KEY", err)
	}
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatalf("error path JSON is not valid: %v", err)
	}
	if dj.APIKeySet {
		t.Error("api_key_set = true, want false when key is missing")
	}
	if !strings.Contains(dj.Status, "DEEPSEEK_API_KEY") {
		t.Errorf("status = %q, want it to contain DEEPSEEK_API_KEY", dj.Status)
	}
}

func TestDoctorJSONExitCodesMatchHuman(t *testing.T) {
	// When API key is set but agent file parse produces diagnostics,
	// both human and JSON paths must return the same error (non-nil).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	// Write a malformed agent file to trigger agent diagnostics.
	agentsDir := filepath.Join(workspace, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	malformedPath := filepath.Join(agentsDir, "badagent.toml")
	if err := os.WriteFile(malformedPath, []byte("invalid toml {{\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Run human path.
	var hOut, hErrOut strings.Builder
	humanErr := runDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &hOut, &hErrOut)

	// Run JSON path.
	var jOut, jErrOut strings.Builder
	jsonErr := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &jOut, &jErrOut)

	// Both paths must agree on whether an error was returned.
	if (humanErr == nil) != (jsonErr == nil) {
		t.Errorf("exit code mismatch: human err=%v, json err=%v", humanErr, jsonErr)
	}
	// If human path returns error, JSON path must also return error.
	if humanErr != nil && jsonErr == nil {
		t.Errorf("human path returned error %q but JSON path returned nil", humanErr.Error())
	}
}

func TestDoctorJSONUnknownFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--bogus", "--workspace", workspace}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown flag error, got: %v", err)
	}
	if out.Len() > 0 {
		t.Errorf("stdout should be empty on unknown flag, got: %s", out.String())
	}
}

func TestDoctorJSONAgentCatalogEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "local", "name = \"local\"\ndescription = \"safe agent\"\n")
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "coder", "name = \"coder\"\ndescription = \"code helper\"\ntools = [\"read_file\", \"write_file\"]\nmax_turns = 5\n")
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("doctor --json unexpected error: %v", err)
	}
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}
	if len(dj.AgentCatalog) == 0 {
		t.Fatal("agent_catalog is empty, expected entries")
	}
	// Verify agent entries have required fields.
	for _, entry := range dj.AgentCatalog {
		if entry.Name == "" {
			t.Error("agent entry has empty name")
		}
		if entry.Description == "" {
			t.Errorf("agent %q has empty description", entry.Name)
		}
	}
	// Verify specific agents exist.
	found := map[string]bool{}
	for _, entry := range dj.AgentCatalog {
		found[entry.Name] = true
	}
	if !found["local"] {
		t.Error("agent 'local' not found in catalog")
	}
	if !found["coder"] {
		t.Error("agent 'coder' not found in catalog")
	}
}

func TestDoctorJSONDescriptionSanitized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	// Agent with description >200 chars.
	longDesc := strings.Repeat("x", 250)
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "longdesc",
		"name = \"longdesc\"\ndescription = \""+longDesc+"\"\n")
	// Agent with description containing excessive whitespace and a tab
	// character (written as TOML escape \t, which parses to a literal tab).
	// SanitizeDescription replaces tabs with spaces and strips control chars.
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "noisydesc",
		"name = \"noisydesc\"\ndescription = \"hello   world  \t  test   \"\n")
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("doctor --json unexpected error: %v", err)
	}
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}
	for _, entry := range dj.AgentCatalog {
		if entry.Name == "longdesc" {
			runes := []rune(entry.Description)
			if len(runes) > 200 {
				t.Errorf("longdesc description len=%d, want <=200", len(runes))
			}
		}
		if entry.Name == "noisydesc" {
			// SanitizeDescription should have stripped whitespace/control chars.
			for _, r := range entry.Description {
				if r < 0x20 || r == 0x7f {
					t.Errorf("noisydesc description contains control char U+%04X", r)
				}
			}
		}
	}
}

func TestDoctorJSONEmptyAgentCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir() // No agents directory.
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("doctor --json unexpected error: %v", err)
	}
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}
	if dj.AgentCatalog == nil {
		t.Error("agent_catalog should be empty array, not nil")
	}
	if len(dj.AgentCatalog) != 0 {
		t.Errorf("agent_catalog len=%d, want 0", len(dj.AgentCatalog))
	}
}

// runDoctorJSON runs doctor with --json and decodes the JSON output.
// It fails the test when the command errors or the output is not JSON.
func runDoctorJSON(t *testing.T, configPath, workspace string) doctorJSON {
	t.Helper()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatal(err)
	}
	return dj
}

func TestDoctorJSONEnvFileStates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")

	t.Run("no_env_file", func(t *testing.T) {
		root := t.TempDir()
		dj := runDoctorJSON(t, writeDoctorConfig(t, root), t.TempDir())
		if dj.EnvFileLoaded {
			t.Error("env_file_loaded should be false when no env file exists")
		}
		if dj.EnvFile == "(none)" && dj.EnvFileLoaded {
			t.Error("env_file should be (none) and loaded false")
		}
	})

	t.Run("env_file_loaded", func(t *testing.T) {
		root := t.TempDir()
		// An explicit .env file makes loadEnvMap load it (default candidates
		// do not include test temp directories).
		envPath := filepath.Join(root, ".env")
		if err := os.WriteFile(envPath, []byte("DEEPSEEK_API_KEY=example-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		dj := runDoctorJSON(t, writeDoctorConfigWithEnvPath(t, root, envPath), t.TempDir())
		if !dj.EnvFileLoaded {
			t.Error("env_file_loaded should be true when .env exists and is loaded")
		}
	})

	t.Run("env_file_not_loaded", func(t *testing.T) {
		root := t.TempDir()
		// No env_file in config and no candidate .env: the JSON path must
		// read the same EnvFileUsed/EnvFilePath fields the human path uses.
		dj := runDoctorJSON(t, writeDoctorConfig(t, root), t.TempDir())
		if dj.EnvFileLoaded {
			t.Error("env_file_loaded should be false when no env file candidate exists")
		}
	})
}

func TestDoctorJSONNoJSONInHumanPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := out.String()
	// No JSON brackets at start of lines.
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "}") {
			t.Errorf("human output contains JSON bracket line: %q", line)
		}
	}
	// No JSON-specific keys.
	if strings.Contains(text, `"api_key_set"`) {
		t.Error(`human output contains JSON key "api_key_set"`)
	}
	if strings.Contains(text, `"agent_catalog"`) {
		t.Error(`human output contains JSON key "agent_catalog"`)
	}
}

func TestDoctorJSONWithJSONEqualsValue(t *testing.T) {
	// --json=anything should be rejected by the unknown-flag catch.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--config", configPath, "--json=yes", "--workspace", workspace}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown flag error for --json=yes, got: %v", err)
	}
}

// TestDoctorJSONLoadError verifies the JSON output when config.Load fails
// (e.g. no config file at all with AllowMissingConfig). The
// writeDoctorJSONLoadError path writes a hardcoded doctorJSON with status
// "configuration diagnostics unavailable".
func TestDoctorJSONLoadError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	// No config file, no --config flag. AllowMissingConfig is true inside
	// runDoctorWithIO, but loadFile returns (File{}, "", false, nil) when
	// no config is found. Then resolveLoaded returns an error because no
	// provider models are available. This triggers the writeDoctorJSONLoadError
	// path inside runDoctorWithIO.
	// Use separate temp dirs so no config file exists in either.
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runDoctorWithIO([]string{"--json", "--workspace", workspace}, &out, &errOut)
	// Config load should fail because no config file is found.
	if err == nil {
		t.Fatal("expected error when no config file exists")
	}
	if !strings.Contains(err.Error(), "configuration diagnostics unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The JSON output must be valid and contain expected fields.
	var dj doctorJSON
	if err := json.Unmarshal([]byte(out.String()), &dj); err != nil {
		t.Fatalf("load-error JSON is not valid: %v\nraw: %s", err, out.String())
	}
	if dj.Config != "(unavailable)" {
		t.Errorf("config = %q, want (unavailable)", dj.Config)
	}
	if dj.Status != "configuration diagnostics unavailable" {
		t.Errorf("status = %q, want configuration diagnostics unavailable", dj.Status)
	}
	if dj.APIKeySet {
		t.Error("api_key_set should be false when config is unavailable")
	}
	// Verify all required top-level keys are present.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out.String()), &raw); err != nil {
		t.Fatal(err)
	}
	requiredKeys := []string{
		"config", "env_file", "env_file_loaded", "provider", "model",
		"model_catalog", "base_url", "api_key_env", "api_key_set",
		"agent_catalog", "warnings", "status",
	}
	for _, key := range requiredKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing required key %q in load-error JSON output", key)
		}
	}
}
