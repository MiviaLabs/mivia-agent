package cli

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

func runDoctor(args []string) error {
	cfgPath, rest, _ := flagValue(args, "--config")
	if len(rest) > 0 {
		return fmt.Errorf("doctor: unexpected arguments: %v", rest)
	}
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}

	fmt.Printf("mivia doctor\n")
	fmt.Printf("  config:     %s\n", displayPath(res.ConfigPath))
	if res.EnvFileUsed {
		fmt.Printf("  env_file:   %s (loaded)\n", displayPath(res.EnvFilePath))
	} else if res.EnvFilePath != "" {
		fmt.Printf("  env_file:   %s (not loaded)\n", displayPath(res.EnvFilePath))
	} else {
		fmt.Printf("  env_file:   (none found; using process env only)\n")
	}
	fmt.Printf("  provider:   %s\n", res.ProviderName)
	fmt.Printf("  model:      %s\n", res.Model)
	fmt.Printf("  base_url:   %s\n", res.BaseURL)
	fmt.Printf("  api_key_env:%s\n", res.APIKeyEnv)
	if res.APIKeySet {
		fmt.Printf("  api_key:    set (value redacted)\n")
	} else {
		fmt.Printf("  api_key:    MISSING — set %s in environment or env file\n", res.APIKeyEnv)
		fmt.Fprintf(os.Stderr, "doctor: not ready for chat\n")
		return fmt.Errorf("missing %s", res.APIKeyEnv)
	}
	if res.ProviderName == "deepseek" {
		descriptor, _ := providerregistry.Lookup(res.ProviderName)
		fmt.Printf("  note:       default model is %s; use %s for harder tasks (--model or config)\n",
			descriptor.DefaultModel, config.DeepSeekProModel)
	}
	fmt.Printf("  status:     ok\n")
	return nil
}

func displayPath(p string) string {
	if p == "" {
		return "(none)"
	}
	return p
}
