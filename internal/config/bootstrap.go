package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
	"golang.org/x/term"
)

// DefaultUserConfigTOML is the minimal config content written for a
// first-time user when no config file exists anywhere. It selects the
// shipped default provider (openrouter) and its default model, matching the
// shipped example at .mivia/mivia.toml.example. internal/cli/setup.go (the
// explicit `mivia setup` command) and loadFile's silent auto-bootstrap path
// (LoadOptions.AutoBootstrapUserConfig) both write this exact content, so
// there is one source of truth for "what a brand-new user's config looks
// like" - see docs/plans/first-run-onboarding-plan.md section 2.1.
const DefaultUserConfigTOML = `[provider]
name = "openrouter"

[providers.openrouter]
models = [{ name = "openai/gpt-5.6-luna", context_window_tokens = 400000 }]
`

// WriteDefaultUserConfig writes DefaultUserConfigTOML to path, creating the
// parent directory (0700) if needed. It does not check whether path already
// exists - callers that must not overwrite an existing file need to Stat it
// themselves first (see internal/cli/setup.go's writeSetupConfigIfMissing
// and this file's own autoBootstrapUserConfig).
func WriteDefaultUserConfig(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(DefaultUserConfigTOML), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// autoBootstrapUserConfig silently writes a minimal default config to
// UserConfigPath() and returns the path it wrote. It returns "" (no error)
// when HOME cannot be resolved, in which case the caller falls back to its
// existing AllowMissingConfig behavior.
//
// It is only reached from loadFile when LoadOptions.AutoBootstrapUserConfig
// is set and no explicit --config/$MIVIA_CONFIG path was given and the
// normal candidate search already came up empty - see loadFile's doc
// comment for the full policy - so it never overwrites an existing file.
func autoBootstrapUserConfig() (string, error) {
	path := UserConfigPath()
	if path == "" {
		return "", nil
	}
	if err := WriteDefaultUserConfig(path); err != nil {
		return "", fmt.Errorf("auto-bootstrap user config: %w", err)
	}
	return path, nil
}

// WriteUserEnvKey writes key=value into the env file at path, preserving any
// existing keys, atomically and with 0600 permissions so the key never
// appears in a world-readable file. Shared by `mivia setup` and the
// mivia-chat first-run key prompt (internal/clichat) so there is one place
// that knows how a key gets persisted to disk.
func WriteUserEnvKey(path, key, value string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	entries := map[string]string{}
	if existing, err := sdkenvfile.Load(path); err == nil {
		entries = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	entries[key] = value

	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var body strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&body, "%s=%s\n", k, entries[k])
	}

	tmp, err := os.CreateTemp(dir, ".mivia-env-*")
	if err != nil {
		return fmt.Errorf("create a temp env file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set env file permissions: %w", err)
	}
	if _, err := tmp.WriteString(body.String()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the env file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the env file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("install the env file: %w", err)
	}
	return nil
}

// PromptAPIKey prompts for a provider's API key on stdout/stdin with the
// input masked (golang.org/x/term), mirroring the exact prompt `mivia setup`
// has always used. Callers must confirm stdin/stdout are both a TTY (see
// IsInteractiveTTY) before calling this - it does not check itself, so
// calling it against non-interactive stdin will block waiting for input.
// Returns the trimmed key, which may be empty if the user enters nothing.
func PromptAPIKey(stdout io.Writer, stdin io.Reader, provider, keyEnv string) (string, error) {
	f, ok := stdin.(*os.File)
	if !ok {
		return "", fmt.Errorf("PromptAPIKey: stdin is not a terminal-capable file")
	}
	fmt.Fprintf(stdout, "Enter the API key for provider %q (%s): ", provider, keyEnv)
	raw, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return "", fmt.Errorf("read the API key: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// IsInteractiveTTY reports whether f is an *os.File attached to a terminal.
// Shared helper for the `mivia setup` prompt gate and mivia chat's first-run
// auto-setup gate so both use the identical check. f is typically an
// io.Reader (stdin) or io.Writer (stdout); it is checked structurally
// rather than typed as one or the other so one helper serves both.
func IsInteractiveTTY(f any) bool {
	file, ok := f.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
