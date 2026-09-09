package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeRepoConfigAt writes body to root's own .mivia/mivia.toml, returning
// its path. writeUserConfig (agents_test.go) is the sibling helper for the
// user-level ~/.mivia/mivia.toml.
func writeRepoConfigAt(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// deepseekProviderTOML builds a [providers.deepseek] stanza with one model
// named modelName, at contextWindow tokens.
func deepseekProviderTOML(modelName string, contextWindow int) string {
	return strings.Join([]string{
		"[providers.deepseek]",
		"models = [{ name = " + strconv.Quote(modelName) + ", context_window_tokens = " + strconv.Itoa(contextWindow) + " }]",
		"",
	}, "\n")
}

// openrouterProviderTOML builds a [providers.openrouter] stanza with one
// model.
func openrouterProviderTOML(modelName string, contextWindow int) string {
	return strings.Join([]string{
		"[providers.openrouter]",
		"models = [{ name = " + strconv.Quote(modelName) + ", context_window_tokens = " + strconv.Itoa(contextWindow) + " }]",
		"",
	}, "\n")
}

// (a) provider-less repo config + user config with deepseek provider.
func TestMergeProviderFallbackFillsProviderlessRepoConfig(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	root := t.TempDir()
	repoPath := writeRepoConfigAt(t, root, "[workflows]\n")
	writeUserConfig(t, home, strings.Join([]string{
		"[provider]",
		"name = " + strconv.Quote("deepseek"),
		"",
		deepseekProviderTOML("deepseek-chat", 131072),
	}, "\n"))

	res, err := Load(LoadOptions{ConfigPath: repoPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "deepseek" {
		t.Fatalf("ProviderName = %q, want deepseek", res.ProviderName)
	}
	if len(res.Models) == 0 || res.Models[0] != "deepseek-chat" {
		t.Fatalf("Models = %v, want [deepseek-chat]", res.Models)
	}
}

// (b) precedence: repo's own [providers.deepseek] wins entirely over the
// user's; the user's openrouter (absent from repo) is filled in.
func TestMergeProviderFallbackRepoStanzaWinsEntirely(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	root := t.TempDir()
	repoPath := writeRepoConfigAt(t, root, strings.Join([]string{
		"[provider]",
		"name = " + strconv.Quote("deepseek"),
		"",
		deepseekProviderTOML("model-A", 131072),
	}, "\n"))
	writeUserConfig(t, home, strings.Join([]string{
		deepseekProviderTOML("model-B", 64000),
		openrouterProviderTOML("or-model", 128000),
	}, "\n"))

	res, err := Load(LoadOptions{ConfigPath: repoPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "deepseek" {
		t.Fatalf("ProviderName = %q, want deepseek", res.ProviderName)
	}
	if len(res.Models) != 1 || res.Models[0] != "model-A" {
		t.Fatalf("active deepseek catalog = %v, want only [model-A] (repo stanza must win entirely)", res.Models)
	}
	var sawOpenrouter bool
	for _, group := range res.ModelCatalog() {
		if group.Provider == "openrouter" {
			sawOpenrouter = true
			if len(group.Models) != 1 || group.Models[0].Name != "or-model" {
				t.Fatalf("openrouter group models = %v, want [or-model]", group.Models)
			}
		}
	}
	if !sawOpenrouter {
		t.Fatal("catalog missing openrouter group filled from user config")
	}
}

// (c) name-without-stanza, user has the stanza: resolves against the user's
// stanza.
func TestMergeProviderFallbackFillsMissingStanzaForNamedProvider(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	root := t.TempDir()
	repoPath := writeRepoConfigAt(t, root, "[provider]\nname = \"deepseek\"\n")
	writeUserConfig(t, home, deepseekProviderTOML("deepseek-chat", 131072))

	res, err := Load(LoadOptions{ConfigPath: repoPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "deepseek" || len(res.Models) != 1 || res.Models[0] != "deepseek-chat" {
		t.Fatalf("res = %+v", res)
	}
}

// (d) name-without-stanza, user lacks it too -> new absent-stanza message.
func TestMergeProviderFallbackAbsentStanzaErrorsWithNewMessage(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	root := t.TempDir()
	repoPath := writeRepoConfigAt(t, root, "[provider]\nname = \"deepseek\"\n")
	writeUserConfig(t, home, openrouterProviderTOML("or-model", 128000))

	_, err := Load(LoadOptions{ConfigPath: repoPath})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "is not configured") || !strings.Contains(err.Error(), `"deepseek"`) {
		t.Fatalf("error = %v, want the absent-stanza message naming deepseek", err)
	}
	if strings.Contains(err.Error(), "models must be non-empty") {
		t.Fatalf("error = %v, want the new absent-stanza message, not the old one", err)
	}
}

// (e) no user config at all + provider-less repo config -> new absent-
// stanza message (stanza absent for the default provider), not the old one.
func TestMergeProviderFallbackNoUserConfigUsesAbsentStanzaMessage(t *testing.T) {
	isolateHomeAndConfigEnv(t)
	root := t.TempDir()
	repoPath := writeRepoConfigAt(t, root, "[workflows]\n")

	_, err := Load(LoadOptions{ConfigPath: repoPath, AllowMissingConfig: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("error = %v, want the absent-stanza message", err)
	}
	if strings.Contains(err.Error(), "models must be non-empty") {
		t.Fatalf("error = %v, want the new absent-stanza message, not the old one", err)
	}
}

// (f) [provider] per-field fill: repo's prompt_cache="off" survives even
// though the user config supplies the provider name/stanza.
func TestMergeProviderFallbackPerFieldFillPreservesRepoPromptCache(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	root := t.TempDir()
	repoPath := writeRepoConfigAt(t, root, "[provider]\nprompt_cache = \"off\"\n")
	writeUserConfig(t, home, strings.Join([]string{
		"[provider]",
		"name = " + strconv.Quote("deepseek"),
		"",
		deepseekProviderTOML("deepseek-chat", 131072),
	}, "\n"))

	res, err := Load(LoadOptions{ConfigPath: repoPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "deepseek" {
		t.Fatalf("ProviderName = %q, want deepseek (from user config)", res.ProviderName)
	}
	if res.PromptCache != "off" {
		t.Fatalf("PromptCache = %q, want off (preserved from repo config)", res.PromptCache)
	}
}

// (g) base path IS the user config: no self-merge weirdness.
func TestMergeProviderFallbackNoOpWhenBaseIsUserConfig(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	writeUserConfig(t, home, strings.Join([]string{
		"[provider]",
		"name = " + strconv.Quote("deepseek"),
		"",
		deepseekProviderTOML("deepseek-chat", 131072),
	}, "\n"))

	res, err := Load(LoadOptions{ConfigPath: UserConfigPath()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "deepseek" || len(res.Models) != 1 || res.Models[0] != "deepseek-chat" {
		t.Fatalf("res = %+v", res)
	}
}

// TestMergeProviderFallbackSkipsUnresolvableHome pins the no-home path: when
// HOME is unset the user layer has no address, so the fallback contributes
// nothing and never turns a good explicit config into a load failure.
func TestMergeProviderFallbackSkipsUnresolvableHome(t *testing.T) {
	root := t.TempDir()
	repoPath := writeRepoConfigAt(t, root, strings.Join([]string{
		"[provider]",
		"name = " + strconv.Quote("deepseek"),
		"",
		deepseekProviderTOML("deepseek-chat", 131072),
	}, "\n"))
	t.Setenv("MIVIA_CONFIG", "")
	t.Setenv("HOME", "")
	if got := UserConfigPath(); got != "" {
		t.Fatalf("UserConfigPath() = %q, want empty with HOME unset", got)
	}

	var file File
	if err := mergeProviderFallback(&file, repoPath); err != nil {
		t.Fatalf("mergeProviderFallback with no resolvable home: %v", err)
	}
	if file.Provider.Name != "" || len(file.Providers) != 0 {
		t.Fatalf("file = %+v, want no user-layer contribution", file.Provider)
	}

	res, err := Load(LoadOptions{ConfigPath: repoPath})
	if err != nil {
		t.Fatalf("Load with no resolvable home: %v", err)
	}
	if res.ProviderName != "deepseek" {
		t.Fatalf("ProviderName = %q, want the explicit config's deepseek", res.ProviderName)
	}
}
