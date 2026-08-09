package memory

import (
	"fmt"
	"strings"
	"unicode"
)

// maxOrgIDLen bounds a configured org identity. Enough for host + org path
// segments (for example "github.com/MiviaLabs").
const maxOrgIDLen = 128

// NormalizeOrgID validates and normalizes a user-supplied org identity.
//
// The identity is a plain string column in the org store; it never becomes a
// filesystem path. It may contain letters, digits, dots, hyphens, underscores
// and slashes (host/org), is case-insensitive, and is stored lowercase.
func NormalizeOrgID(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", fmt.Errorf("org_id is empty")
	}
	if len(trimmed) > maxOrgIDLen {
		return "", fmt.Errorf("org_id is too long (max %d characters)", maxOrgIDLen)
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") {
		return "", fmt.Errorf("org_id must not start or end with a slash")
	}
	if strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("org_id must not contain \"..\"")
	}
	for _, r := range trimmed {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("org_id must not contain whitespace or control characters")
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == '/':
		default:
			return "", fmt.Errorf("org_id contains unsupported character %q", r)
		}
	}
	return strings.ToLower(trimmed), nil
}
