package cli

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func runDoctor(args []string) error {
	return runDoctorWithIO(args, os.Stdout, os.Stderr)
}

func runDoctorWithIO(args []string, stdout, stderr io.Writer) error {
	cfgPath, workspaceRoot, err := parseDoctorArgs(args)
	if err != nil {
		return err
	}
	view, catalogErr := loadAgentCatalog(workspaceRoot)
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		AllowMissingConfig: true,
	})
	if err != nil {
		fmt.Fprintln(stdout, "mivia doctor")
		fmt.Fprintln(stdout, "  config:     unavailable")
		if catalogErr == nil {
			writeAgentCatalog(stdout, view, stderr)
		} else {
			fmt.Fprintln(stdout, "agents:")
			fmt.Fprintln(stdout, "  state: unavailable")
		}
		return fmt.Errorf("configuration diagnostics unavailable")
	}

	fmt.Fprintln(stdout, "mivia doctor")
	fmt.Fprintf(stdout, "  config:     %s\n", displayPath(res.ConfigPath))
	if res.EnvFileUsed {
		fmt.Fprintf(stdout, "  env_file:   %s (loaded)\n", displayPath(res.EnvFilePath))
	} else if res.EnvFilePath != "" {
		fmt.Fprintf(stdout, "  env_file:   %s (not loaded)\n", displayPath(res.EnvFilePath))
	} else {
		fmt.Fprintln(stdout, "  env_file:   (none found; using process env only)")
	}
	fmt.Fprint(stdout, formatDoctorModelInfo(res))
	fmt.Fprintf(stdout, "  base_url:   %s\n", safeDoctorURL(res.BaseURL))
	fmt.Fprintf(stdout, "  api_key_env:%s\n", safeCatalogText(res.APIKeyEnv, 128))

	if catalogErr != nil {
		fmt.Fprintln(stdout, "agents:")
		fmt.Fprintln(stdout, "  state: unavailable")
		fmt.Fprintln(stderr, "doctor: agent diagnostics unavailable")
	} else {
		writeAgentCatalog(stdout, view, stderr)
		for _, warning := range view.Report.Warnings {
			fmt.Fprintln(stderr, "warning:", warning)
		}
	}

	var diagnostics string
	if catalogErr == nil {
		diagnostics = view.Report.DiagnosticSummary()
		if diagnostics != "none" {
			fmt.Fprintf(stderr, "doctor: agent diagnostics: %s\n", diagnostics)
		}
	}
	if res.APIKeySet {
		fmt.Fprintln(stdout, "  api_key:    set (value redacted)")
	} else {
		fmt.Fprintf(stdout, "  api_key:    MISSING - set %s in environment or env file\n", res.APIKeyEnv)
		fmt.Fprintln(stderr, "doctor: not ready for chat")
		if diagnostics != "" && diagnostics != "none" {
			return fmt.Errorf("agent diagnostics: %s; missing %s", diagnostics, res.APIKeyEnv)
		}
		return fmt.Errorf("missing %s", res.APIKeyEnv)
	}
	if diagnostics != "" && diagnostics != "none" {
		return fmt.Errorf("agent diagnostics: %s", diagnostics)
	}
	fmt.Fprintln(stdout, "  status:     ok")
	return nil
}

func safeDoctorURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "(invalid)"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	value := parsed.String()
	return safeCatalogText(value, 240)
}

func parseDoctorArgs(args []string) (configPath, workspaceRoot string, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return "", "", fmt.Errorf("doctor: --config requires a path")
			}
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
			if strings.TrimSpace(configPath) == "" || strings.HasPrefix(configPath, "-") {
				return "", "", fmt.Errorf("doctor: --config requires a path")
			}
		case args[i] == "--workspace":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return "", "", fmt.Errorf("doctor: --workspace requires a directory")
			}
			workspaceRoot = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--workspace="):
			workspaceRoot = strings.TrimPrefix(args[i], "--workspace=")
			if strings.TrimSpace(workspaceRoot) == "" || strings.HasPrefix(workspaceRoot, "-") {
				return "", "", fmt.Errorf("doctor: --workspace requires a directory")
			}
		case strings.HasPrefix(args[i], "-"):
			return "", "", fmt.Errorf("doctor: unknown flag %q", safeCatalogText(args[i], 80))
		default:
			return "", "", fmt.Errorf("doctor: unexpected arguments (%d)", len(args)-i)
		}
	}
	return configPath, workspaceRoot, nil
}

func formatDoctorModelInfo(res *config.Resolved) string {
	var out strings.Builder
	fmt.Fprintf(&out, "  provider:   %s\n  model:      %s\n", res.ProviderName, res.Model)
	if catalog := res.ModelCatalog(); len(catalog) > 0 {
		fmt.Fprintf(&out, "  catalog:    %s\n", formatModelCatalog(catalog, ", ", "; "))
	} else if len(res.Models) > 0 {
		fmt.Fprintf(&out, "  models:     %s\n", strings.Join(res.Models, ", "))
	}
	return out.String()
}

func formatModelCatalog(catalog []config.ProviderModelGroup, modelSep, groupSep string) string {
	groups := make([]string, 0, len(catalog))
	for _, group := range catalog {
		models := make([]string, 0, len(group.Models))
		for _, model := range group.Models {
			models = append(models, group.Provider+"/"+model.Name+":"+strconv.Itoa(model.ContextWindowTokens))
		}
		if len(models) == 0 {
			groups = append(groups, group.Provider+":(none)")
			continue
		}
		groups = append(groups, strings.Join(models, modelSep))
	}
	return strings.Join(groups, groupSep)
}

func displayPath(p string) string {
	if p == "" {
		return "(none)"
	}
	return safeCatalogText(p, 240)
}
