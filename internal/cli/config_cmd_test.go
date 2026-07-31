package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestFormatConfigShowModelPolicy(t *testing.T) {
	managed := formatConfigShow(&config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}})
	if !strings.Contains(managed, "models=A,B\nmodel_policy=restricted\n") {
		t.Fatalf("managed output = %q", managed)
	}
	unrestricted := formatConfigShow(&config.Resolved{ProviderName: "p", Model: "A"})
	if strings.Contains(unrestricted, "models=") || !strings.Contains(unrestricted, "model_policy=unrestricted\n") {
		t.Fatalf("unrestricted output = %q", unrestricted)
	}
}

func TestFormatDoctorModelInfo(t *testing.T) {
	got := formatDoctorModelInfo(&config.Resolved{ProviderName: "deepseek", Model: "A", Models: []string{"A", "B"}})
	if !strings.Contains(got, "  models:     A, B\n") || strings.Contains(got, "note:") {
		t.Fatalf("doctor info = %q", got)
	}
}
