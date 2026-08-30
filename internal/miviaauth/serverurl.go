package miviaauth

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
)

// DefaultServerURL is the mivia API root used when MIVIA_API_BASE_URL is
// unset. Override it for local or staging use -- a local API is typically
// http://localhost:3001.
//
// This is the API ROOT only. The /v1 version prefix belongs to the request
// paths (see client.go), so an override never carries a version.
const DefaultServerURL = "https://api.mivia.app"

// serverURLEnvKey is the env var ServerURLFromEnv resolves.
const serverURLEnvKey = "MIVIA_API_BASE_URL"

// ServerURLFromEnv returns MIVIA_API_BASE_URL if set and non-blank, else
// DefaultServerURL. The process environment wins; ./.env and ~/.mivia/.env
// are consulted as a fallback, matching how provider API keys are resolved
// elsewhere in this repo (internal/config/load.go, internal/config.Lookup)
// -- without this, a value set only in ~/.mivia/.env would be silently
// invisible here, since that file is never loaded into the OS environment.
func ServerURLFromEnv() string {
	envMap := map[string]string{}
	if path, ok := config.FirstExisting(config.DefaultEnvCandidates()); ok {
		if m, err := sdkenvfile.Load(path); err == nil {
			envMap = m
		}
	}
	if v, ok := config.Lookup(serverURLEnvKey, envMap); ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return DefaultServerURL
}
