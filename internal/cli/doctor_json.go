package cli

import (
	"encoding/json"
	"io"
	"strings"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// doctorJSON is the JSON output structure for `mivia doctor --json`.
// The API key VALUE must never appear; only api_key_set (bool) and
// api_key_env (name) are included. key_required (bool) distinguishes
// "no key needed" (ollama loopback) from "key missing".
type doctorJSON struct {
	Config        string           `json:"config"`
	EnvFile       string           `json:"env_file"`
	EnvFileLoaded bool             `json:"env_file_loaded"`
	Provider      string           `json:"provider"`
	Model         string           `json:"model"`
	ModelCatalog  string           `json:"model_catalog"`
	BaseURL       string           `json:"base_url"`
	APIKeyEnv     string           `json:"api_key_env"`
	APIKeySet     bool             `json:"api_key_set"`
	KeyRequired   bool             `json:"key_required"`
	AgentCatalog  []jsonAgentEntry `json:"agent_catalog"`
	Warnings      []string         `json:"warnings"`
	Status        string           `json:"status"`
}

// jsonAgentEntry is one agent entry in the JSON doctor output.
type jsonAgentEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Model       string   `json:"model"`
	Tools       []string `json:"tools"`
	MaxTurns    string   `json:"max_turns"`
}

// mapModelCatalogJSON produces a JSON-renderable model catalog string from
// []config.ProviderModelGroup, reusing formatModelCatalog's formatting logic.
func mapModelCatalogJSON(catalog []config.ProviderModelGroup) string {
	return formatModelCatalog(catalog, ", ", "; ")
}

// parseAgentToolsList converts the comma-separated tool string from the
// agent catalog row back into a []string for JSON serialization.
func parseAgentToolsList(tools string) []string {
	if tools == "(none)" {
		return []string{}
	}
	parts := strings.Split(tools, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// noUntrustedInputFuzzNote documents why no fuzz gate is requested:
// the --json feature serializes Go structs to JSON via encoding/json with
// no external decoder. The only user input is parseDoctorArgs which already
// rejects unknown flags. A fuzz target would only exercise encoding/json's
// marshal path, which is well-tested by the Go standard library.
var noUntrustedInputFuzzNote = struct{}{}

// writeDoctorJSON builds and marshals the doctorJSON struct and writes it
// to stdout as a single JSON value, newline-terminated.
func writeDoctorJSON(stdout io.Writer, res *config.Resolved, view agentCatalogView, catalogErr error, statusErr error) {
	dj := doctorJSON{
		Config:        displayPath(res.ConfigPath),
		EnvFile:       displayPath(res.EnvFilePath),
		EnvFileLoaded: res.EnvFileUsed,
		Provider:      res.ProviderName,
		Model:         res.Model,
		ModelCatalog:  mapModelCatalogJSON(res.ModelCatalog()),
		BaseURL:       safeDoctorURL(res.BaseURL),
		APIKeyEnv:     cliagents.SafeCatalogText(res.APIKeyEnv, 128),
		APIKeySet:     res.APIKeySet,
		KeyRequired:   !(res.ProviderName == "ollama" && config.IsOllamaLoopback(res.BaseURL)),
		AgentCatalog:  []jsonAgentEntry{},
		Warnings:      []string{},
	}

	// Build agent catalog entries.
	for _, row := range view.Rows {
		tools := parseAgentToolsList(row.Tools)
		dj.AgentCatalog = append(dj.AgentCatalog, jsonAgentEntry{
			Name:        row.Name,
			Description: row.Description,
			Model:       row.Model,
			Tools:       tools,
			MaxTurns:    row.Turns,
		})
	}

	// Collect warnings.
	if catalogErr == nil {
		dj.Warnings = append(dj.Warnings, view.Report.Warnings...)
	}

	// Determine status.
	if statusErr != nil {
		dj.Status = statusErr.Error()
	} else {
		dj.Status = "ok"
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(dj)
}

// writeDoctorJSONLoadError handles JSON output when config.Load fails.
func writeDoctorJSONLoadError(stdout io.Writer) {
	dj := doctorJSON{
		Config:        "(unavailable)",
		EnvFile:       "",
		EnvFileLoaded: false,
		Provider:      "",
		Model:         "",
		ModelCatalog:  "",
		BaseURL:       "",
		APIKeyEnv:     "",
		APIKeySet:     false,
		KeyRequired:   true,
		AgentCatalog:  []jsonAgentEntry{},
		Warnings:      []string{},
		Status:        "configuration diagnostics unavailable",
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(dj)
}
