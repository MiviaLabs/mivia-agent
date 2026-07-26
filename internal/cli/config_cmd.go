package cli

import (
	"fmt"

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
	fmt.Printf("config_path=%s\n", res.ConfigPath)
	fmt.Printf("env_file=%s\n", displayPath(res.EnvFilePath))
	fmt.Printf("env_file_loaded=%v\n", res.EnvFileUsed)
	fmt.Printf("provider=%s\n", res.ProviderName)
	fmt.Printf("model=%s\n", res.Model)
	fmt.Printf("base_url=%s\n", res.BaseURL)
	fmt.Printf("api_key_env=%s\n", res.APIKeyEnv)
	fmt.Printf("api_key_set=%v\n", res.APIKeySet)
	if res.HTTPReferer != "" {
		fmt.Printf("http_referer=%s\n", res.HTTPReferer)
	}
	if res.XTitle != "" {
		fmt.Printf("x_title=%s\n", res.XTitle)
	}
	if res.SystemPrompt != "" {
		fmt.Printf("system_prompt=(set, %d chars)\n", len(res.SystemPrompt))
	}
	return nil
}
