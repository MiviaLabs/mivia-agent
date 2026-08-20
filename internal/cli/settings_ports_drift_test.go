package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestSettingsPortsCoverConfigFields is the drift guard
// docs/design/settings-screen.md §7 calls for. internal/uikit/ports
// re-declares config-shaped types (MCPServerView, ProviderView,
// ModelView, AgentView) rather than importing internal/config directly
// - the right call for a secret-free projection, but nothing then
// notices when internal/config grows a field the settings screen never
// learns about. This test lives here, not under internal/uikit, so it
// can import internal/config without dragging that dependency into the
// UI-isolated packages themselves (internal/cli already depends on
// internal/config; depending on the leaf internal/uikit/ports package
// too is one-directional and adds nothing uikit/ui can see).
//
// A source field absent from every candidate view name AND absent from
// the matching ignore list fails the test, naming the field. Renaming
// or removing a field is a green diff; adding one is red until this
// test is told about it, which is the whole point.
func TestSettingsPortsCoverConfigFields(t *testing.T) {
	cases := []struct {
		name   string
		source reflect.Type
		views  []reflect.Type
		ignore map[string]string // field -> why it is deliberately absent
	}{
		{
			name:   "MCPServerConfig",
			source: reflect.TypeOf(config.MCPServerConfig{}),
			views:  []reflect.Type{reflect.TypeOf(ports.MCPServerView{})},
			ignore: map[string]string{
				"URL":     "splits into MCPServerView.Endpoint (host-only) at projection; the full URL can carry userinfo/query secrets - settings-screen.md §5",
				"Env":     "projected as EnvNames (env var NAMES only, never values)",
				"Headers": "projected as HeaderEnvNames (env var NAMES only, never values)",
			},
		},
		{
			name:   "ProviderConfig",
			source: reflect.TypeOf(config.ProviderConfig{}),
			views:  []reflect.Type{reflect.TypeOf(ports.ProviderView{})},
			ignore: map[string]string{
				"Models":       "projected as []ModelView, checked separately below",
				"DefaultModel": "projected as ActiveModel",
				"LegacyModel":  "decode-time sentinel for a rejected legacy TOML key; not runtime state",
				"HTTPReferer":  "provider-specific request header, not a setting a user edits here",
				"XTitle":       "provider-specific request header, not a setting a user edits here",
			},
		},
		{
			name:   "ModelSpec",
			source: reflect.TypeOf(config.ModelSpec{}),
			views:  []reflect.Type{reflect.TypeOf(ports.ModelView{})},
			ignore: map[string]string{
				"ReasoningDialect": "internal wire-shape detail; config_cmd.go's own comment says a picker does not need it",
			},
		},
		{
			name:   "AgentFileSpec",
			source: reflect.TypeOf(config.AgentFileSpec{}),
			views:  []reflect.Type{reflect.TypeOf(ports.AgentView{})},
			ignore: map[string]string{
				"Inherits":        "resolved before the view is built; AgentView carries the resolved Provider/Model/Tools, not the inheritance chain",
				"AllowEmptyTools": "a validation flag on the source TOML, not a value a settings screen edits",
				"ToolsAdd":        "resolved into the final Tools list",
				"ToolsRemove":     "resolved into the final Tools list",
				"DisallowedTools": "resolved into the final Tools list",
				"ToolsCore":       "resolved into the final Tools list",
				"OutputSchema":    "not yet surfaced; no consumer in this screen's v1 scope",
				"InputSchema":     "not yet surfaced; no consumer in this screen's v1 scope",
				"SystemPrompt":    "projected as SystemPromptChars (a length, never the text) - settings-screen.md §5",
				"TimeoutSeconds":  "not yet surfaced; no consumer in this screen's v1 scope",
				"MaxTokens":       "not yet surfaced; no consumer in this screen's v1 scope",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			viewFields := map[string]bool{}
			for _, v := range c.views {
				for i := 0; i < v.NumField(); i++ {
					viewFields[strings.ToLower(v.Field(i).Name)] = true
				}
			}
			for i := 0; i < c.source.NumField(); i++ {
				name := c.source.Field(i).Name
				if _, ok := c.ignore[name]; ok {
					continue
				}
				if !viewFields[strings.ToLower(name)] {
					t.Errorf("config.%s.%s has no matching field in %v and no ignore entry - "+
						"add it to a ports view, or document why it is deliberately absent",
						c.name, name, c.views)
				}
			}
		})
	}
}
