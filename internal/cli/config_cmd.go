package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func runConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mivia config show")
	}
	switch args[0] {
	case "show":
		return runConfigShow(args[1:])
	default:
		return fmt.Errorf("unknown config subcommand %q (try show)", args[0])
	}
}

func runConfigShow(args []string) error {
	cfgPath, rest, _ := flagValue(args, "--config")
	if len(rest) > 0 {
		return fmt.Errorf("config show: unexpected arguments: %v", rest)
	}
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}
	fmt.Print(formatConfigShow(res))
	return nil
}

func formatConfigShow(res *config.Resolved) string {
	var out strings.Builder
	fmt.Fprintf(&out, "config_path=%s\n", res.ConfigPath)
	fmt.Fprintf(&out, "env_file=%s\n", displayPath(res.EnvFilePath))
	fmt.Fprintf(&out, "env_file_loaded=%v\n", res.EnvFileUsed)
	fmt.Fprintf(&out, "provider=%s\n", res.ProviderName)
	fmt.Fprintf(&out, "model=%s\n", res.Model)
	if catalog := res.ModelCatalog(); len(catalog) > 0 {
		fmt.Fprintf(&out, "model_catalog=%s\n", formatModelCatalog(catalog, ",", ";"))
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
	fmt.Fprintf(&out, "base_url=%s\n", res.BaseURL)
	fmt.Fprintf(&out, "api_key_env=%s\n", res.APIKeyEnv)
	fmt.Fprintf(&out, "api_key_set=%v\n", res.APIKeySet)
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
