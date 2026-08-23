package cli

import (
	"errors"
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
)

// setupDefaultConfig is the minimal config written when no config exists and
// the provider is the shipped default (openrouter). Other providers need
// their own [providers.<name>] block; setup writes only the API key for them.
const setupDefaultConfig = `[provider]
name = "openrouter"

[providers.openrouter]
models = [{ name = "openai/gpt-5.6-luna", context_window_tokens = 400000 }]
`

// setupOptions holds the parsed `mivia setup` flags.
type setupOptions struct {
	provider string
	key      string
	envFile  string
	config   string
	yes      bool
}

// runSetup guides a new user through provider key placement. It writes the API
// key to an env file (0600) and, when no config exists, a minimal default
// config. It never prints the key value.
func runSetup(args []string) error {
	return runSetupWithIO(args, os.Stdout, os.Stdin)
}

func runSetupWithIO(args []string, stdout io.Writer, stdin io.Reader) error {
	opts, err := parseSetupArgs(args)
	if err != nil {
		return err
	}
	envPath := opts.envFile
	if envPath == "" {
		envPath = config.UserEnvPath()
		if envPath == "" {
			return fmt.Errorf("setup: cannot resolve the user env path")
		}
	}
	cfgPath := opts.config
	if cfgPath == "" {
		cfgPath = config.UserConfigPath()
		if cfgPath == "" {
			return fmt.Errorf("setup: cannot resolve the user config path")
		}
	}

	provider := opts.provider
	if provider == "" {
		provider = defaultSetupProvider()
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	keyEnv := strings.ToUpper(provider) + "_API_KEY"

	key := opts.key
	if key == "" {
		if v, ok := os.LookupEnv(keyEnv); ok && strings.TrimSpace(v) != "" {
			key = strings.TrimSpace(v)
		}
	}
	if key == "" {
		if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			fmt.Fprintf(stdout, "Enter the API key for provider %q (%s): ", provider, keyEnv)
			raw, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(stdout)
			if err != nil {
				return fmt.Errorf("setup: read the API key: %w", err)
			}
			key = strings.TrimSpace(string(raw))
		}
	}
	keylessOllama := key == "" && provider == "ollama"
	if key == "" && !keylessOllama {
		return fmt.Errorf("setup: no API key; pass --key or set %s", keyEnv)
	}

	if key != "" {
		if err := writeSetupEnvFile(envPath, keyEnv, key); err != nil {
			return err
		}
	}
	cfgWritten, err := writeSetupConfigIfMissing(cfgPath, provider)
	if err != nil {
		return err
	}

	return printSetupSummary(stdout, provider, keyEnv, envPath, cfgPath, keylessOllama, cfgWritten)
}

// printSetupSummary renders the post-run summary. It never prints the key value.
func printSetupSummary(stdout io.Writer, provider, keyEnv, envPath, cfgPath string, keylessOllama, cfgWritten bool) error {
	fmt.Fprintln(stdout, "mivia setup")
	fmt.Fprintf(stdout, "  provider:   %s\n", provider)
	fmt.Fprintf(stdout, "  key env:    %s\n", keyEnv)
	if keylessOllama {
		fmt.Fprintln(stdout, "  mode:       local daemon - no API key needed (set base_url to http://127.0.0.1:11434/v1 in [providers.ollama])")
		fmt.Fprintf(stdout, "  mode:       Ollama Cloud - needs the key (default base_url https://ollama.com/v1); pass --key or set %s\n", keyEnv)
	} else {
		fmt.Fprintf(stdout, "  key file:   %s (written)\n", cliorchestrate.DisplayPath(envPath))
	}
	if cfgWritten {
		fmt.Fprintf(stdout, "  config:     %s (written)\n", cliorchestrate.DisplayPath(cfgPath))
	} else if provider != config.DefaultProvider {
		fmt.Fprintf(stdout, "  config:     %s (untouched; add a [providers.%s] block to select it)\n", cliorchestrate.DisplayPath(cfgPath), provider)
	} else {
		fmt.Fprintf(stdout, "  config:     %s (existing)\n", cliorchestrate.DisplayPath(cfgPath))
	}
	if keylessOllama {
		fmt.Fprintln(stdout, "  next:       add a [providers.ollama] block to your config, then run mivia doctor")
	} else {
		fmt.Fprintln(stdout, "  next:       run `mivia doctor` to verify")
	}
	return nil
}

// defaultSetupProvider returns the active provider from an existing config, or
// the shipped default when no config loads.
func defaultSetupProvider() string {
	res, err := config.Load(config.LoadOptions{AllowMissingConfig: true})
	if err == nil && res.ProviderName != "" {
		return res.ProviderName
	}
	return config.DefaultProvider
}

// writeSetupEnvFile writes key=value into path, preserving existing keys.
// It writes atomically with 0600 permissions so the key never appears in a
// world-readable file.
func writeSetupEnvFile(path, key, value string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("setup: create %s: %w", dir, err)
	}
	entries := map[string]string{}
	if existing, err := sdkenvfile.Load(path); err == nil {
		entries = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("setup: read %s: %w", path, err)
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

	tmp, err := os.CreateTemp(dir, ".mivia-setup-*")
	if err != nil {
		return fmt.Errorf("setup: create a temp env file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setup: set env file permissions: %w", err)
	}
	if _, err := tmp.WriteString(body.String()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setup: write the env file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("setup: close the env file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("setup: install the env file: %w", err)
	}
	return nil
}

// writeSetupConfigIfMissing writes the minimal default config when no config
// exists and the provider is the shipped default. It reports whether it wrote
// a file. Other providers keep their configs untouched.
func writeSetupConfigIfMissing(path, provider string) (bool, error) {
	if provider != config.DefaultProvider {
		return false, nil
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("setup: stat %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("setup: create %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(setupDefaultConfig), 0o644); err != nil {
		return false, fmt.Errorf("setup: write %s: %w", path, err)
	}
	return true, nil
}

func parseSetupArgs(args []string) (setupOptions, error) {
	opts := setupOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--yes":
			opts.yes = true
		case arg == "--provider":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("setup: --provider requires a name")
			}
			opts.provider = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--provider="):
			opts.provider = strings.TrimSpace(strings.TrimPrefix(arg, "--provider="))
			if opts.provider == "" {
				return opts, fmt.Errorf("setup: --provider requires a name")
			}
		case arg == "--key":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("setup: --key requires a value")
			}
			opts.key = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--key="):
			opts.key = strings.TrimSpace(strings.TrimPrefix(arg, "--key="))
			if opts.key == "" {
				return opts, fmt.Errorf("setup: --key requires a value")
			}
		case arg == "--env-file":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("setup: --env-file requires a path")
			}
			opts.envFile = args[i+1]
			i++
		case strings.HasPrefix(arg, "--env-file="):
			opts.envFile = strings.TrimPrefix(arg, "--env-file=")
			if strings.TrimSpace(opts.envFile) == "" {
				return opts, fmt.Errorf("setup: --env-file requires a path")
			}
		case arg == "--config":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("setup: --config requires a path")
			}
			opts.config = args[i+1]
			i++
		case strings.HasPrefix(arg, "--config="):
			opts.config = strings.TrimPrefix(arg, "--config=")
			if strings.TrimSpace(opts.config) == "" {
				return opts, fmt.Errorf("setup: --config requires a path")
			}
		case arg == "--help" || arg == "-h":
			return opts, fmt.Errorf("usage: mivia setup [--provider name] [--key value] [--env-file path] [--config path] [--yes]")
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("setup: unknown flag %q", arg)
		default:
			return opts, fmt.Errorf("setup: unexpected argument %q", arg)
		}
	}
	return opts, nil
}
