package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func runConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mivia config show")
	}
	switch args[0] {
	case "show":
		return runConfigShow(args[1:], os.Stdout)
	default:
		return fmt.Errorf("unknown config subcommand %q (try show)", args[0])
	}
}

func runConfigShow(args []string, stdout io.Writer) error {
	cfgPath, rest, _, err := flagValue(args, "--config")
	if err != nil {
		return err
	}
	jsonFlag := false
	var positional []string
	for _, arg := range rest {
		if arg == "--json" {
			jsonFlag = true
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) > 0 {
		return fmt.Errorf("config show: unexpected arguments: %v", positional)
	}
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}
	if jsonFlag {
		return writeConfigShowJSON(stdout, res)
	}
	fmt.Fprint(stdout, formatConfigShow(res))
	return nil
}

// configShowJSON is the --json shape of "config show" - a secret-free
// snapshot of the active provider/model and the full selectable catalog, for
// a caller (e.g. mivia-agent-desktop) that wants to show or offer a model
// picker without shelling out again per keystroke. No API key, no
// system_prompt (present in the text form only as a length), no dialect
// (an internal wire-shape detail, not something a model picker needs).
type configShowJSON struct {
	Provider string                   `json:"provider"`
	Model    string                   `json:"model"`
	Catalog  []providerModelGroupJSON `json:"catalog,omitempty"`
}

type providerModelGroupJSON struct {
	Provider       string          `json:"provider"`
	Active         bool            `json:"active"`
	Selectable     bool            `json:"selectable"`
	DisabledReason string          `json:"disabled_reason,omitempty"`
	Models         []modelSpecJSON `json:"models"`
}

type modelSpecJSON struct {
	Name                string   `json:"name"`
	ContextWindowTokens int      `json:"context_window_tokens"`
	MaxOutputTokens     int      `json:"max_output_tokens,omitempty"`
	ReasoningEfforts    []string `json:"reasoning_efforts,omitempty"`
	Reasoning           string   `json:"reasoning,omitempty"`
}

func writeConfigShowJSON(w io.Writer, res *config.Resolved) error {
	out := configShowJSON{
		Provider: res.ProviderName,
		Model:    res.Model,
	}
	for _, group := range res.ModelCatalog() {
		models := make([]modelSpecJSON, 0, len(group.Models))
		for _, spec := range group.Models {
			efforts := make([]string, 0, len(spec.ReasoningEfforts))
			for _, level := range spec.ReasoningEfforts {
				efforts = append(efforts, string(level))
			}
			models = append(models, modelSpecJSON{
				Name:                spec.Name,
				ContextWindowTokens: spec.ContextWindowTokens,
				MaxOutputTokens:     spec.MaxOutputTokens,
				ReasoningEfforts:    efforts,
				Reasoning:           string(spec.Reasoning),
			})
		}
		out.Catalog = append(out.Catalog, providerModelGroupJSON{
			Provider:       group.Provider,
			Active:         group.Active,
			Selectable:     group.Selectable,
			DisabledReason: group.DisabledReason,
			Models:         models,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func formatConfigShow(res *config.Resolved) string {
	var out strings.Builder
	fmt.Fprintf(&out, "config_path=%s\n", res.ConfigPath)
	fmt.Fprintf(&out, "env_file=%s\n", cliorchestrate.DisplayPath(res.EnvFilePath))
	fmt.Fprintf(&out, "env_file_loaded=%v\n", res.EnvFileUsed)
	fmt.Fprintf(&out, "provider=%s\n", res.ProviderName)
	fmt.Fprintf(&out, "model=%s\n", res.Model)
	if catalog := res.ModelCatalog(); len(catalog) > 0 {
		fmt.Fprintf(&out, "model_catalog=%s\n", cliorchestrate.FormatModelCatalog(catalog, ",", ";"))
		fmt.Fprintln(&out, "model_policy=explicit-catalog")
		fmt.Fprintf(&out, "active_prompt_budget=%d\n", res.MaxContextTokens)
	} else {
		if len(res.Models) > 0 {
			fmt.Fprintf(&out, "models=%s\n", strings.Join(res.Models, ","))
		}
		policy := "unrestricted"
		if len(res.Models) > 0 {
			policy = "restricted"
		}
		fmt.Fprintf(&out, "model_policy=%s\n", policy)
	}
	if advisory := cliorchestrate.PromptBudgetAdvisory(res); advisory != "" {
		fmt.Fprintf(&out, "prompt_budget_advisory=%s\n", advisory)
	}
	fmt.Fprintf(&out, "base_url=%s\n", res.BaseURL)
	fmt.Fprintf(&out, "api_key_env=%s\n", res.APIKeyEnv)
	fmt.Fprintf(&out, "api_key_set=%v\n", res.APIKeySet)
	fmt.Fprintf(&out, "api_key_required=%v\n", !(res.ProviderName == "ollama" && config.IsOllamaLoopback(res.BaseURL)))
	if res.HTTPReferer != "" {
		fmt.Fprintf(&out, "http_referer=%s\n", res.HTTPReferer)
	}
	if res.XTitle != "" {
		fmt.Fprintf(&out, "x_title=%s\n", res.XTitle)
	}
	if res.SystemPrompt != "" {
		fmt.Fprintf(&out, "system_prompt=(set, %d chars)\n", len(res.SystemPrompt))
	}
	return out.String()
}
