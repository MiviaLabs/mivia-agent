package vcs

import (
	"strings"
	"unicode"
)

// MaxWorktreeNameLen is the maximum length of a sanitised worktree name.
// Git imposes no hard limit, but filesystems and ergonomics do.
const MaxWorktreeNameLen = 64

// reservedNames are names that must not be used as worktree directories.
var reservedNames = map[string]bool{
	".": true, "..": true, ".git": true, ".mivia": true,
}

// SanitizeName converts a user-provided worktree name to a safe directory name.
// Rules:
//   - Trim whitespace
//   - Lowercase
//   - Replace runs of non-alphanumeric characters with a single hyphen
//   - Strip leading/trailing hyphens
//   - Truncate to MaxWorktreeNameLen
//   - Reject if empty or reserved after sanitisation
type InvalidNameError struct {
	Input  string
	Reason string
}

func (e InvalidNameError) Error() string {
	return "invalid worktree name: " + e.Reason
}

func SanitizeName(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", InvalidNameError{Input: input, Reason: "name is empty"}
	}
	// Reject exact reserved names before sanitisation strips leading dots.
	if reservedNames[input] {
		return "", InvalidNameError{Input: input, Reason: "name is reserved"}
	}
	var b strings.Builder
	var prevHyphen bool
	for _, r := range input {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			prevHyphen = false
		case r == '-' || r == '_':
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > MaxWorktreeNameLen {
		name = name[:MaxWorktreeNameLen]
	}
	// Re-trim after truncation may have left a trailing hyphen.
	name = strings.TrimRight(name, "-")
	if name == "" {
		return "", InvalidNameError{Input: input, Reason: "name is empty after sanitisation"}
	}
	if reservedNames[name] {
		return "", InvalidNameError{Input: input, Reason: "name is reserved"}
	}
	return name, nil
}
