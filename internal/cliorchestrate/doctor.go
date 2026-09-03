package cliorchestrate

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// ollamaLoopbackKeyless reports whether the active provider is ollama
// pointed at a loopback daemon, where no API key is required.
func ollamaLoopbackKeyless(res *config.Resolved) bool {
	return res != nil && res.ProviderName == "ollama" && config.IsOllamaLoopback(res.BaseURL)
}

// RunDoctor runs the doctor command with the given args, writing to stdout/stderr.
func RunDoctor(args []string) error {
	return RunDoctorWithIO(args, os.Stdout, os.Stderr)
}

// RunDoctorWithIO runs the doctor command with explicit writers for testability.
func RunDoctorWithIO(args []string, stdout, stderr io.Writer) error {
	cfgPath, workspaceRoot, jsonMode, err := parseDoctorArgs(args)
	if err != nil {
		return err
	}
	view, catalogErr := cliagents.LoadAgentCatalog(workspaceRoot)
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		WorkspaceRoot:      workspaceRoot,
		AllowMissingConfig: true,
	})
	if err != nil {
		if jsonMode {
			writeDoctorJSONLoadError(stdout)
		} else {
			writeDoctorHumanLoadError(stdout, stderr, view, catalogErr)
		}
		return fmt.Errorf("configuration diagnostics unavailable")
	}

	statusErr := doctorStatusErr(res, view, catalogErr)
	if jsonMode {
		writeDoctorJSON(stdout, res, view, catalogErr, statusErr)
		writeDoctorDiagnostics(stderr, catalogErr, view, res)
		return statusErr
	}
	writeDoctorHuman(stdout, stderr, res, view, catalogErr, statusErr)
	return statusErr
}

// doctorStatusErr computes the doctor exit-status error: nil means "ok".
func doctorStatusErr(res *config.Resolved, view cliagents.AgentCatalogView, catalogErr error) error {
	var diagnostics string
	if catalogErr == nil {
		diagnostics = view.Report.DiagnosticSummary()
	}
	if res.APIKeySet || ollamaLoopbackKeyless(res) {
		if diagnostics != "" && diagnostics != "none" {
			return fmt.Errorf("agent diagnostics: %s", diagnostics)
		}
		return nil
	}
	if diagnostics != "" && diagnostics != "none" {
		return fmt.Errorf("agent diagnostics: %s; missing %s", diagnostics, res.APIKeyEnv)
	}
	return fmt.Errorf("missing %s", res.APIKeyEnv)
}

// PromptBudgetAdvisory reports the session's prompt budget and where it came
// from. Empty only when there is no budget to report.
//
// It used to call an unset [chat] max_prompt_tokens "unbounded" and recommend
// a fixed cap. Both were wrong. The budget is never unbounded: with the knob
// unset it is the bound model's own window minus its output reserve, which is
// a bound. And recommending one number for every model told the operator of a
// 1M-window model to throw away most of the capacity they are paying for,
// which is the opposite of a diagnosis.
//
// What is worth flagging is the reverse: a cap that holds the budget far below
// what the model can do, since that is invisible everywhere else and reads as
// a smaller model. The threshold matches ports.ModelInfo.BudgetIsCapped, so
// the sidebar and the doctor call the same configuration capped.
func PromptBudgetAdvisory(res *config.Resolved) string {
	if res == nil || res.MaxContextTokens <= 0 {
		return ""
	}
	if res.MaxPromptTokens == nil {
		// "from the model window" rather than "window minus output reserve":
		// a model that declares no output cap has no reserve subtracted, and
		// the budget is then the window itself.
		return fmt.Sprintf("%d tokens (from the model window)", res.MaxContextTokens)
	}
	window := activeModelWindow(res)
	if window > 0 && int64(res.MaxContextTokens)*2 < int64(window) {
		return fmt.Sprintf("%d tokens (capped by [chat] max_prompt_tokens; the model window is %d)",
			res.MaxContextTokens, window)
	}
	return fmt.Sprintf("%d tokens (capped by [chat] max_prompt_tokens)", res.MaxContextTokens)
}

// activeModelWindow is the declared context window of the bound model, or 0
// when the catalog does not describe it. Read from the catalog rather than
// carried on Resolved so it always reflects the model actually selected.
func activeModelWindow(res *config.Resolved) int {
	for _, group := range res.ModelCatalog() {
		if group.Provider != res.ProviderName {
			continue
		}
		for _, model := range group.Models {
			if model.Name == res.Model {
				return model.ContextWindowTokens
			}
		}
	}
	return 0
}

// writeDoctorHumanLoadError prints the load-failure screen (human path).
func writeDoctorHumanLoadError(stdout, stderr io.Writer, view cliagents.AgentCatalogView, catalogErr error) {
	fmt.Fprintln(stdout, "mivia doctor")
	fmt.Fprintln(stdout, "  config:     unavailable")
	if catalogErr == nil {
		cliagents.WriteAgentCatalog(stdout, view, stderr)
	} else {
		fmt.Fprintln(stdout, "agents:")
		fmt.Fprintln(stdout, "  state: unavailable")
	}
}

// writeDoctorDiagnostics writes the stderr side-effects shared by both paths:
// catalog warnings, agent diagnostics, and the not-ready notice.
func writeDoctorDiagnostics(stderr io.Writer, catalogErr error, view cliagents.AgentCatalogView, res *config.Resolved) {
	if catalogErr != nil {
		fmt.Fprintln(stderr, "doctor: agent diagnostics unavailable")
		return
	}
	for _, warning := range view.Report.Warnings {
		fmt.Fprintln(stderr, "warning:", warning)
	}
	if diagnostics := view.Report.DiagnosticSummary(); diagnostics != "" && diagnostics != "none" {
		fmt.Fprintf(stderr, "doctor: agent diagnostics: %s\n", diagnostics)
	}
	if !res.APIKeySet && !ollamaLoopbackKeyless(res) {
		fmt.Fprintln(stderr, "doctor: not ready for chat")
	}
}

// writeDoctorHuman prints the byte-identical human diagnostics screen.
func writeDoctorHuman(stdout, stderr io.Writer, res *config.Resolved, view cliagents.AgentCatalogView, catalogErr error, statusErr error) {
	fmt.Fprintln(stdout, "mivia doctor")
	fmt.Fprintf(stdout, "  config:     %s\n", DisplayPath(res.ConfigPath))
	if res.EnvFileUsed {
		fmt.Fprintf(stdout, "  env_file:   %s (loaded)\n", DisplayPath(res.EnvFilePath))
	} else if res.EnvFilePath != "" {
		fmt.Fprintf(stdout, "  env_file:   %s (not loaded)\n", DisplayPath(res.EnvFilePath))
	} else {
		fmt.Fprintln(stdout, "  env_file:   (none found; using process env only)")
	}
	fmt.Fprint(stdout, FormatDoctorModelInfo(res))
	if advisory := PromptBudgetAdvisory(res); advisory != "" {
		fmt.Fprintf(stdout, "  prompt_budget: %s\n", advisory)
	}
	fmt.Fprintf(stdout, "  base_url:   %s\n", safeDoctorURL(res.BaseURL))
	fmt.Fprintf(stdout, "  api_key_env:%s\n", cliagents.SafeCatalogText(res.APIKeyEnv, 128))
	writeDoctorSyncHuman(stdout, doctorSync(res))

	if catalogErr != nil {
		fmt.Fprintln(stdout, "agents:")
		fmt.Fprintln(stdout, "  state: unavailable")
		fmt.Fprintln(stderr, "doctor: agent diagnostics unavailable")
	} else {
		cliagents.WriteAgentCatalog(stdout, view, stderr)
		for _, warning := range view.Report.Warnings {
			fmt.Fprintln(stderr, "warning:", warning)
		}
	}

	if diagnostics := view.Report.DiagnosticSummary(); diagnostics != "" && diagnostics != "none" {
		fmt.Fprintf(stderr, "doctor: agent diagnostics: %s\n", diagnostics)
	}
	if res.APIKeySet {
		fmt.Fprintln(stdout, "  api_key:    set (value redacted)")
	} else if ollamaLoopbackKeyless(res) {
		fmt.Fprintln(stdout, "  api_key:    not required (local daemon)")
	} else {
		fmt.Fprintf(stdout, "  api_key:    MISSING - set %s in environment or env file\n", res.APIKeyEnv)
		fmt.Fprintln(stderr, "doctor: not ready for chat")
	}
	if statusErr == nil {
		fmt.Fprintln(stdout, "  status:     ok")
	}
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
	return cliagents.SafeCatalogText(value, 240)
}

func parseDoctorArgs(args []string) (configPath, workspaceRoot string, jsonMode bool, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			jsonMode = true
		case args[i] == "--config":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return "", "", false, fmt.Errorf("doctor: --config requires a path")
			}
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
			if strings.TrimSpace(configPath) == "" || strings.HasPrefix(configPath, "-") {
				return "", "", false, fmt.Errorf("doctor: --config requires a path")
			}
		case args[i] == "--workspace":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return "", "", false, fmt.Errorf("doctor: --workspace requires a directory")
			}
			workspaceRoot = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--workspace="):
			workspaceRoot = strings.TrimPrefix(args[i], "--workspace=")
			if strings.TrimSpace(workspaceRoot) == "" || strings.HasPrefix(workspaceRoot, "-") {
				return "", "", false, fmt.Errorf("doctor: --workspace requires a directory")
			}
		case strings.HasPrefix(args[i], "-"):
			return "", "", false, fmt.Errorf("doctor: unknown flag %q", cliagents.SafeCatalogText(args[i], 80))
		default:
			return "", "", false, fmt.Errorf("doctor: unexpected arguments (%d)", len(args)-i)
		}
	}
	return configPath, workspaceRoot, jsonMode, nil
}

func FormatDoctorModelInfo(res *config.Resolved) string {
	var out strings.Builder
	fmt.Fprintf(&out, "  provider:   %s\n  model:      %s\n", res.ProviderName, res.Model)
	if catalog := res.ModelCatalog(); len(catalog) > 0 {
		fmt.Fprintf(&out, "  catalog:    %s\n", FormatModelCatalog(catalog, ", ", "; "))
	} else if len(res.Models) > 0 {
		fmt.Fprintf(&out, "  models:     %s\n", strings.Join(res.Models, ", "))
	}
	return out.String()
}

func FormatModelCatalog(catalog []config.ProviderModelGroup, modelSep, groupSep string) string {
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

func DisplayPath(p string) string {
	if p == "" {
		return "(none)"
	}
	return cliagents.SafeCatalogText(p, 240)
}
