package miviaauth

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/envfile"
)

// DefaultServerURL is the go-mivia API root used when MIVIA_API_BASE_URL is
// unset. The production API is not live yet; override for local/staging use.
const DefaultServerURL = "https://api.mivia.app"

// serverURLEnvKey is the env var ServerURLFromEnv resolves.
const serverURLEnvKey = "MIVIA_API_BASE_URL"

// ServerURLFromEnv returns MIVIA_API_BASE_URL if set and non-blank, else
// DefaultServerURL. The process environment wins; ./.env and ~/.mivia/.env
// are consulted as a fallback, matching how provider API keys are resolved
// elsewhere in this repo (internal/config/load.go, internal/envfile.Lookup)
// -- without this, a value set only in ~/.mivia/.env would be silently
// invisible here, since that file is never loaded into the OS environment.
func ServerURLFromEnv() string {
	envMap := map[string]string{}
	if path, ok := config.FirstExisting(config.DefaultEnvCandidates()); ok {
		if m, err := envfile.Load(path); err == nil {
			envMap = m
		}
	}
	if v, ok := envfile.Lookup(serverURLEnvKey, envMap); ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return DefaultServerURL
}
