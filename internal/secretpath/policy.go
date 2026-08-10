// Package secretpath matches configured workspace secret paths.
package secretpath

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Policy is an immutable secret-path policy.
type Policy struct {
	patterns   []string
	exceptions map[string]struct{}
}

// New makes a policy from configured substring patterns and exact exceptions.
func New(patterns, exceptions []string) (Policy, error) {
	policy := Policy{patterns: make([]string, 0, len(patterns)), exceptions: make(map[string]struct{}, len(exceptions))}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return Policy{}, fmt.Errorf("secret path pattern is empty")
		}
		// Store patterns in the same canonical form Match derives from rel
		// (lowercase, forward slashes, Clean) so a pattern with a leading "./",
		// trailing "/", "." / ".." segment, or backslashes still matches the
		// cleaned workspace-relative path (DC-11). The TrimSpace + empty check
		// must run first because filepath.Clean("") == ".".
		policy.patterns = append(policy.patterns, normalizePath(pattern))
	}
	for _, exception := range exceptions {
		normalized, err := normalizeException(exception)
		if err != nil {
			return Policy{}, err
		}
		policy.exceptions[normalized] = struct{}{}
	}
	return policy, nil
}

// Match reports whether rel matches a configured secret pattern.
func (p Policy) Match(rel string) bool {
	normalized := normalizePath(rel)
	if normalized == "" {
		return false
	}
	if _, ok := p.exceptions[normalized]; ok {
		return false
	}
	for _, pattern := range p.patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func normalizeException(value string) (string, error) {
	value = strings.TrimSpace(value)
	// Both path conventions: on Windows, "/x" is not filepath.IsAbs (no drive
	// letter) but still escapes the workspace root the way a leading "/" does
	// on Unix, so it must be rejected too.
	if value == "" || filepath.IsAbs(value) || path.IsAbs(value) {
		return "", fmt.Errorf("secret path exception is invalid")
	}
	normalized := normalizePath(value)
	if normalized == "" || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("secret path exception is invalid")
	}
	return normalized, nil
}

func normalizePath(value string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(value))))
}
