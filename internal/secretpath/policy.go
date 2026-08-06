// Package secretpath matches configured workspace secret paths.
package secretpath

import (
	"fmt"
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
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			return Policy{}, fmt.Errorf("secret path pattern is empty")
		}
		policy.patterns = append(policy.patterns, pattern)
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
	if value == "" || filepath.IsAbs(value) {
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
