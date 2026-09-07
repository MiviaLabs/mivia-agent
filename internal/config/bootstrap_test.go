package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateHomeAndConfigEnv points HOME at a fresh temp dir and clears
// $MIVIA_CONFIG, so DefaultConfigCandidates() finds nothing - the same
// isolation TestLoadDefaultsDeepSeekFlash uses.
func isolateHomeAndConfigEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	return home
}

// TestLoadAutoBootstrapsUserConfigWhenMissing pins the core silent-bootstrap
// behavior: with AutoBootstrapUserConfig set and no config file discoverable
// anywhere, loadFile writes DefaultUserConfigTOML to UserConfigPath() and
// proceeds as if that file had existed (found=true).
func TestLoadAutoBootstrapsUserConfigWhenMissing(t *testing.T) {
	isolateHomeAndConfigEnv(t)

	res, err := Load(LoadOptions{AllowMissingConfig: true, AutoBootstrapUserConfig: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "openrouter" {
		t.Fatalf("ProviderName = %q, want openrouter", res.ProviderName)
	}
	if res.Model != "openai/gpt-5.6-luna" {
		t.Fatalf("Model = %q, want openai/gpt-5.6-luna", res.Model)
	}

	path := UserConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bootstrapped config %s: %v", path, err)
	}
	if string(data) != DefaultUserConfigTOML {
		t.Fatalf("bootstrapped config content = %q, want %q", data, DefaultUserConfigTOML)
	}
}

// TestLoadWithoutAutoBootstrapLeavesConfigMissing pins that the flag
// defaults to false: every existing config.Load caller that never sets
// AutoBootstrapUserConfig keeps today's found=false, no-file-written
// behavior unchanged.
func TestLoadWithoutAutoBootstrapLeavesConfigMissing(t *testing.T) {
	isolateHomeAndConfigEnv(t)

	_, err := Load(LoadOptions{AllowMissingConfig: true})
	if err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("err = %v, want the no-provider-configured error", err)
	}
	if _, statErr := os.Stat(UserConfigPath()); !os.IsNotExist(statErr) {
		t.Fatalf("expected no config file written, stat err = %v", statErr)
	}
}

// TestLoadExplicitMissingConfigPathStillHardErrors pins that an explicit
// --config/$MIVIA_CONFIG miss is never papered over by auto-bootstrap, even
// with the flag set: auto-bootstrap only fires on the "nothing configured
// anywhere" case.
func TestLoadExplicitMissingConfigPathStillHardErrors(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	missing := filepath.Join(home, "does-not-exist.toml")

	_, err := Load(LoadOptions{ConfigPath: missing, AllowMissingConfig: true, AutoBootstrapUserConfig: true})
	if err == nil {
		t.Fatal("expected a hard error for an explicit missing --config path")
	}
	if strings.Contains(err.Error(), "models must be non-empty") {
		t.Fatalf("err = %v, want a read/stat error naming the missing path, not the bootstrap-succeeded path", err)
	}
	if _, statErr := os.Stat(UserConfigPath()); !os.IsNotExist(statErr) {
		t.Fatalf("expected no user config auto-written for an explicit --config miss, stat err = %v", statErr)
	}
}

// TestLoadAutoBootstrapDoesNotOverwriteExistingConfig pins that auto-
// bootstrap never fires - and never double-writes - when a config already
// exists at UserConfigPath().
func TestLoadAutoBootstrapDoesNotOverwriteExistingConfig(t *testing.T) {
	isolateHomeAndConfigEnv(t)

	path := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{ name = \"deepseek-chat\", context_window_tokens = 64000 }]\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	res, err := Load(LoadOptions{AllowMissingConfig: true, AutoBootstrapUserConfig: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "deepseek" {
		t.Fatalf("ProviderName = %q, want the untouched existing deepseek config", res.ProviderName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if string(data) != existing {
		t.Fatalf("config content changed: got %q, want unchanged %q", data, existing)
	}
}

// TestWriteUserEnvKeyPreservesExistingKeys pins WriteUserEnvKey's merge
// behavior, since both `mivia setup` and mivia chat's first-run key prompt
// depend on it not clobbering unrelated keys.
func TestWriteUserEnvKeyPreservesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := WriteUserEnvKey(path, "FIRST_KEY", "one"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteUserEnvKey(path, "SECOND_KEY", "two"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "FIRST_KEY=one") || !strings.Contains(content, "SECOND_KEY=two") {
		t.Fatalf("content = %q, want both keys preserved", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Exact mode bits are POSIX semantics: on Windows chmod keeps only the
	// read-only attribute and Stat reports 0666. The privacy guarantee there
	// rides on profile/%TEMP% ACL inheritance, not on mode bits.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}
}

// TestAutoBootstrapUserConfigReportsStatFailure pins the non-ErrNotExist stat
// branch: when the user config path cannot be stat'd at all, bootstrap
// reports that failure instead of silently treating the path as absent and
// writing over an unreadable tree. The failure is forced by making the
// ~/.mivia namespace a regular file, so stat of ~/.mivia/mivia.toml fails
// with ENOTDIR rather than ErrNotExist.
func TestAutoBootstrapUserConfigReportsStatFailure(t *testing.T) {
	isolateHomeAndConfigEnv(t)
	namespace := filepath.Dir(UserConfigPath())
	if err := os.WriteFile(namespace, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("write blocking file at %s: %v", namespace, err)
	}

	path, err := autoBootstrapUserConfig()
	if err == nil {
		t.Fatal("expected a stat failure when the config namespace is a regular file")
	}
	if !strings.Contains(err.Error(), "stat ") {
		t.Fatalf("err = %v, want the stat branch, not the write branch", err)
	}
	if path != "" {
		t.Fatalf("path = %q, want empty on a stat failure", path)
	}
	data, readErr := os.ReadFile(namespace)
	if readErr != nil {
		t.Fatalf("re-read %s: %v", namespace, readErr)
	}
	if string(data) != "not a directory\n" {
		t.Fatalf("blocking file content = %q, want it untouched", data)
	}
}
