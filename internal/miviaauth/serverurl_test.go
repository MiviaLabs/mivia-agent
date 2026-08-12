package miviaauth

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestServerURLFromEnvUnsetReturnsDefault(t *testing.T) {
	t.Setenv("MIVIA_API_BASE_URL", "")

	if got := ServerURLFromEnv(); got != DefaultServerURL {
		t.Errorf("ServerURLFromEnv() = %q, want %q", got, DefaultServerURL)
	}
}

func TestServerURLFromEnvSetReturnsOverride(t *testing.T) {
	t.Setenv("MIVIA_API_BASE_URL", "https://api.staging.mivia.app")

	want := "https://api.staging.mivia.app"
	if got := ServerURLFromEnv(); got != want {
		t.Errorf("ServerURLFromEnv() = %q, want %q", got, want)
	}
}

func TestServerURLFromEnvWhitespaceOnlyFallsBackToDefault(t *testing.T) {
	t.Setenv("MIVIA_API_BASE_URL", "   ")

	if got := ServerURLFromEnv(); got != DefaultServerURL {
		t.Errorf("ServerURLFromEnv() = %q, want %q", got, DefaultServerURL)
	}
}

func TestDefaultServerURLIsValidHTTPSURL(t *testing.T) {
	if _, err := config.ValidateHTTPSURL(DefaultServerURL); err != nil {
		t.Errorf("ValidateHTTPSURL(DefaultServerURL) error = %v, want nil", err)
	}
}
