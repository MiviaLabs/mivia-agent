package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type driftTestCase struct {
	name   string
	source reflect.Type
	views  []reflect.Type
	ignore map[string]string
}

// TestSettingsPortsCoverConfigFields is the drift guard that catches a
// config field silently missing from the settings screen's view types: any
// exported source field must either appear in a candidate view or be
// recorded in the test's ignore map with a reason.
func TestSettingsPortsCoverConfigFields(t *testing.T) {
	for _, tc := range driftTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertFieldsCovered(t, tc)
		})
	}
}

func assertFieldsCovered(t *testing.T, tc driftTestCase) {
	t.Helper()
	knownViewFields := make(map[string]bool)
	for _, v := range tc.views {
		for i := 0; i < v.NumField(); i++ {
			knownViewFields[strings.ToLower(v.Field(i).Name)] = true
		}
	}

	for i := 0; i < tc.source.NumField(); i++ {
		sf := tc.source.Field(i)
		if !sf.IsExported() {
			continue
		}
		if _, ignored := tc.ignore[sf.Name]; ignored {
			continue
		}
		if !knownViewFields[strings.ToLower(sf.Name)] {
			t.Errorf("source %s has field %q that is not represented in any candidate view %v and not recorded in the test's ignore map; if the settings screen should show it, add it to ports; if deliberately omitted, document why in the ignore map",
				tc.name, sf.Name, tc.views)
		}
	}
}

func driftTestCases() []driftTestCase {
	return []driftTestCase{
		{
			name:   "MCPServerConfig",
			source: reflect.TypeOf(config.MCPServerConfig{}),
			views:  []reflect.Type{reflect.TypeOf(ports.MCPServerView{})},
			ignore: map[string]string{
				"URL":     "splits into MCPServerView.Endpoint (host-only) at projection; the full URL can carry userinfo/query secrets",
				"Env":     "projected as EnvNames (env var NAMES only, never values)",
				"Headers": "projected as HeaderEnvNames (env var NAMES only, never values)",
			},
		},
		{
			name:   "ProviderConfig",
			source: reflect.TypeOf(config.ProviderConfig{}),
			views:  []reflect.Type{reflect.TypeOf(ports.ProviderView{})},
			ignore: map[string]string{
				"Models":      "projected as []ModelView, checked separately below",
				"LegacyModel": "decode-time sentinel for a rejected legacy TOML key; not runtime state",
				"HTTPReferer": "provider-specific request header, not a setting a user edits here",
				"XTitle":      "provider-specific request header, not a setting a user edits here",
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
				"Inherits":         "resolved before the view is built; AgentView carries the resolved Provider/Model/Tools, not the inheritance chain",
				"AllowEmptyTools":  "a validation flag on the source TOML, not a value a settings screen edits",
				"ToolsAdd":         "resolved before the view is built",
				"ToolsRemove":      "resolved before the view is built",
				"SkillsAdd":        "resolved before the view is built",
				"SkillsRemove":     "resolved before the view is built",
				"MCPServersAdd":    "resolved before the view is built",
				"MCPServersRemove": "resolved before the view is built",
				"DisallowedTools":  "resolved into the final Tools list",
				"ToolsCore":        "resolved into the final Tools list",
				"OutputSchema":     "not yet surfaced; no consumer in this screen's v1 scope",
				"InputSchema":      "not yet surfaced; no consumer in this screen's v1 scope",
				"TimeoutSeconds":   "not yet surfaced; no consumer in this screen's v1 scope",
				"MaxTokens":        "not yet surfaced; no consumer in this screen's v1 scope",
			},
		},
		{
			name:   "ProjectSettings",
			source: reflect.TypeOf(config.ProjectSettings{}),
			views:  []reflect.Type{reflect.TypeOf(ports.ProjectView{})},
			ignore: map[string]string{},
		},
	}
}
