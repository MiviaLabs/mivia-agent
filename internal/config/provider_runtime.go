package config

import (
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/envfile"
)

func resolveProviderRuntimes(file File, envMap map[string]string, active string) (map[string]ProviderRuntime, []ProviderModelGroup) {
	names := make([]string, 0, len(file.Providers))
	for name := range file.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	ordered := append([]string{active}, names...)
	seen := map[string]bool{}
	runtimes := make(map[string]ProviderRuntime, len(names))
	groups := make([]ProviderModelGroup, 0, len(names))
	for _, name := range ordered {
		if seen[name] {
			continue
		}
		seen[name] = true
		pc := file.Providers[name]
		key, keySet := envfile.Lookup(pc.APIKeyEnv, envMap)
		keySetNonBlank := keySet && strings.TrimSpace(key) != ""
		ollamaLoopback := name == "ollama" && IsOllamaLoopback(pc.BaseURL)
		runtimes[name] = ProviderRuntime{
			ProviderName: name, BaseURL: strings.TrimRight(pc.BaseURL, "/"), APIKeyEnv: pc.APIKeyEnv,
			APIKeySet: keySetNonBlank, APIKey: key, HTTPReferer: pc.HTTPReferer,
			XTitle: pc.XTitle, Models: cloneModelSpecs(pc.Models),
		}
		group := ProviderModelGroup{Provider: name, Models: cloneModelSpecs(pc.Models), Active: name == active}
		group.Selectable = len(pc.Models) > 0 && (keySetNonBlank || ollamaLoopback)
		switch {
		case len(pc.Models) == 0:
			group.DisabledReason = "no configured models"
		case !keySetNonBlank:
			if !ollamaLoopback {
				group.DisabledReason = "credential unavailable"
			}
		}
		groups = append(groups, group)
	}
	return runtimes, groups
}
