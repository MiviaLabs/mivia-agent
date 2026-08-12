package miviaauth

import (
	"os"
	"strings"
)

// DefaultServerURL is the go-mivia API root used when MIVIA_API_BASE_URL is
// unset. The production API is not live yet; override for local/staging use.
const DefaultServerURL = "https://api.mivia.app"

// ServerURLFromEnv returns MIVIA_API_BASE_URL if set and non-blank, else
// DefaultServerURL.
func ServerURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("MIVIA_API_BASE_URL")); v != "" {
		return v
	}
	return DefaultServerURL
}
