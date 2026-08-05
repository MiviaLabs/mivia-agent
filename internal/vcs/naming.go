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
//   - Reject names that require truncation
//   - Reject if empty or reserved after sanitisation
type InvalidNameError struct {
	Input  string
	Reason string
}

func (e InvalidNameError) Error() string {
	return "invalid worktree name: " + e.Reason
}

func SanitizeName(input string) (string, error) {
	name, truncated, err := sanitizeName(input)
	if err == nil && truncated {
		return "", InvalidNameError{Input: input, Reason: "name is too long"}
	}
	return name, err
}

func nameIsTruncated(input string) (bool, error) {
	_, truncated, err := sanitizeName(input)
	return truncated, err
}

func sanitizeName(input string) (string, bool, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false, InvalidNameError{Input: input, Reason: "name is empty"}
	}
	// Reject exact reserved names before sanitisation strips leading dots.
	if reservedNames[input] {
		return "", false, InvalidNameError{Input: input, Reason: "name is reserved"}
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
	truncated := false
	if len(name) > MaxWorktreeNameLen {
		name = name[:MaxWorktreeNameLen]
		truncated = true
	}
	// Re-trim after truncation may have left a trailing hyphen.
	name = strings.TrimRight(name, "-")
	if name == "" {
		return "", false, InvalidNameError{Input: input, Reason: "name is empty after sanitisation"}
	}
	// The sanitizer only ever emits letters, digits and hyphens, so the
	// output can never collide with the dotted reserved names checked above;
	// a second reserved-name check here would be dead code.
	return name, truncated, nil
}
