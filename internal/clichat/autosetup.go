package clichat

import (
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// ensureChatAPIKey is the first-chat auto-setup gate: it makes sure a
// resolved config actually has a usable API key before the chat loop starts,
// prompting or bootstrapping one if not. When res already carries a usable
// API key (or is a keyless ollama loopback, mirroring prepareChatStartup's
// own exemption), it returns res unchanged.
//
// Otherwise:
//   - Interactive (stdin AND stdout are both a TTY): prompts for the key
//     using the same masked prompt `mivia setup` uses, writes it to the
//     user env file, then re-resolves config with loadOpts so the freshly
//     written key is used for this run - the caller never has to restart
//     mivia chat a second time.
//   - Non-interactive (scripted, `-p` one-shot, piped, CI): never prompts -
//     blocking on stdin in a non-interactive process is a footgun - and
//     instead fails immediately with an actionable error naming both
//     `mivia setup` and the exact env var to set.
func ensureChatAPIKey(res *config.Resolved, loadOpts config.LoadOptions, stdout io.Writer, stdin io.Reader) (*config.Resolved, error) {
	if res.APIKeySet && strings.TrimSpace(res.APIKey) != "" {
		return res, nil
	}
	if res.ProviderName == "ollama" && config.IsOllamaLoopback(res.BaseURL) {
		return res, nil
	}
	if !config.IsInteractiveTTY(stdin) || !config.IsInteractiveTTY(stdout) {
		return nil, missingAPIKeyErr(res)
	}
	key, err := config.PromptAPIKey(stdout, stdin, res.ProviderName, res.APIKeyEnv)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, missingAPIKeyErr(res)
	}
	envPath := config.UserEnvPath()
	if envPath == "" {
		return nil, fmt.Errorf("chat: cannot resolve the user env path")
	}
	if err := config.WriteUserEnvKey(envPath, res.APIKeyEnv, key); err != nil {
		return nil, err
	}
	refreshed, err := config.Load(loadOpts)
	if err != nil {
		return nil, err
	}
	return refreshed, nil
}

func missingAPIKeyErr(res *config.Resolved) error {
	return fmt.Errorf("missing API key for provider %q — run \"mivia setup\" or set %s", res.ProviderName, res.APIKeyEnv)
}
