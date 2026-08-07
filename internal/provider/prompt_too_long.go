package provider

import (
	"errors"
	"strings"
)

// ErrPromptTooLong is the sentinel wrapped into a provider error when the
// provider rejects a request because the prompt exceeds its context window.
// The agent loop matches it with errors.Is to compact the history to a small
// fixed target and retry exactly once instead of failing the whole run.
var ErrPromptTooLong = errors.New("prompt too long")

// isPromptTooLongMessage classifies a provider's error message as a
// prompt-too-long rejection. It is a classification aid only: the message
// text is read solely to decide whether to attach ErrPromptTooLong and is
// NEVER surfaced in an error string (rule 10: provider content never leaks).
func isPromptTooLongMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, marker := range []string{
		"too long",
		"maximum context",
		"context length",
		"token limit",
		"exceeds the context",
		"reduce the prompt",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
